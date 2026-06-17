package service

import (
	"auto-bot/config"
	"auto-bot/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// Bot инкапсулирует в себе все зависимости, необходимые для работы
type Bot struct {
	Logger    *logrus.Logger
	Storage   Storage
	APIClient APIClient
	cfg       *config.Config
	mu        sync.Mutex
}

// NewBot — конструктор для безопасной инициализации нашего робота
func NewBot(cfg *config.Config, storage Storage, APIClient APIClient, logger *logrus.Logger) *Bot {
	return &Bot{
		cfg:       cfg,
		Storage:   storage,
		APIClient: APIClient,
		Logger:    logger,
	}
}

func (bot *Bot) placeGridOrder(ctx context.Context, side string, price, qty float64, posIdx int) {
	bot.mu.Lock()
	defer bot.mu.Unlock()

	// Используем параметры округления из локального конфига структуры
	pMultiplier := math.Pow(10, float64(bot.cfg.PriceDecimals))
	roundedPrice := math.Round(price*pMultiplier) / pMultiplier
	qMultiplier := math.Pow(10, float64(bot.cfg.QtyDecimals))
	roundedQty := math.Round(qty*qMultiplier) / qMultiplier

	path := "/v5/order/create"
	query := fmt.Sprintf("category=linear&symbol=%s&side=%s&orderType=Limit&qty=%.3f&price=%.1f&positionIdx=%d&timeInForce=GTC",
		bot.cfg.Symbol, side, roundedQty, roundedPrice, posIdx)

	body, err := bot.APIClient.DoSignedRequest("POST", path, query)
	if err != nil {
		bot.Logger.WithError(err).Error("[❌ REST API] Ошибка отправки ордера")
		return
	}

	var apiRes struct {
		RetCode int `json:"retCode"`
		Result  struct {
			OrderID string `json:"orderId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &apiRes); err != nil {
		bot.Logger.WithError(err).Error("[❌ JSON] Ошибка десериализации ответа биржи")
		return
	}

	if apiRes.RetCode == 0 {
		err = bot.Storage.CreateOrder(ctx, models.Order{
			OrderID:     apiRes.Result.OrderID,
			Symbol:      bot.cfg.Symbol,
			Side:        side,
			Price:       roundedPrice,
			Qty:         roundedQty,
			PositionIdx: posIdx,
			Status:      "New",
		})
		if err != nil {
			bot.Logger.WithError(err).Error("[❌ БД] Ошибка записи в SQLite")
			return
		}

		bot.Logger.Infof("[🟢 СЕТКА] Выставлен лимит %s по цене %.1f\n", side, roundedPrice)
	}
}

func (bot *Bot) CheckAndRefreshGrid(ctx context.Context) {
	activeCount, err := bot.Storage.GetCountActiveOrders(ctx)
	if err != nil {
		bot.Logger.WithError(err).Error("[❌ БД] Ошибка проверки активных ордеров")
		return
	}
	if activeCount > 0 {
		return // Ждем выполнения текущей сетки
	}

	candles, err := bot.APIClient.GetKlines(bot.cfg.Symbol, 50)
	if err != nil {
		bot.Logger.WithError(err).Error("[❌ МАРКЕТ] Ошибка получения свечей")
		return
	}

	currentPrice := candles[len(candles)-1].Close
	ema200 := calculateEMA(candles, 30)
	atr := calculateATR(candles, 14)
	buyLevel, sellLevel := findSmartLevels(candles, currentPrice, atr)

	if currentPrice > ema200 {
		fmt.Println("[🔓 LOCK SHORT] Тренд БЫЧИЙ. Выставляем LONG сетку.")
		bot.placeGridOrder(ctx, "Buy", buyLevel, bot.cfg.BaseQty, 1)
		bot.placeGridOrder(ctx, "Buy", buyLevel-(atr*1.5), bot.cfg.BaseQty*1.5, 1)
	} else {
		fmt.Println("[🔒 LOCK LONG] Тренд МЕДВЕЖИЙ. Выставляем SHORT сетку.")
		bot.placeGridOrder(ctx, "Sell", sellLevel, bot.cfg.BaseQty, 2)
		bot.placeGridOrder(ctx, "Sell", sellLevel+(atr*1.5), bot.cfg.BaseQty*1.5, 2)
	}
}

func (bot *Bot) StartWebSocketListener(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var dialer websocket.Dialer
		conn, _, err := dialer.Dial(bot.cfg.WsURL, nil)
		if err != nil {
			bot.Logger.WithError(err).Error("[❌ WS] Ошибка подключения. Повтор через 5 сек... ")
			time.Sleep(5 * time.Second)
			continue
		}

		expires := time.Now().UnixMilli() + 10000
		signature := bot.APIClient.SignParams(bot.cfg.APISecret, fmt.Sprintf("GET/auth%d", expires), bot.cfg.APIKey, "5000", "")
		authMsg := fmt.Sprintf(`{"op":"auth","args":["%s",%d,"%s"]}`, bot.cfg.APIKey, expires, signature)
		err = conn.WriteMessage(websocket.TextMessage, []byte(authMsg))
		if err != nil {
			bot.Logger.WithError(err).Error("[❌ WS] Ошибка подключения")
			continue
		}

		time.Sleep(500 * time.Millisecond)
		err = conn.WriteMessage(websocket.TextMessage, []byte(`{"op":"subscribe","args":["order"]}`))
		if err != nil {
			bot.Logger.WithError(err).Error("[❌ WS] Ошибка подключения")
			continue
		}
		bot.Logger.Info("[🔌 WS] WebSocket Bybit подключен. Мониторинг ордеров запущен.")

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				bot.Logger.WithError(err).Error("[❌ WS] Соединение разорвано. Переподключение...")
				conn.Close()
				break
			}

			var wsUpdate struct {
				Topic string `json:"topic"`
				Data  []struct {
					OrderID     string `json:"orderId"`
					OrderStatus string `json:"orderStatus"`
				} `json:"data"`
			}
			_ = json.Unmarshal(message, &wsUpdate)

			if wsUpdate.Topic == "order" {
				for _, ord := range wsUpdate.Data {
					if ord.OrderStatus == "Filled" {
						bot.mu.Lock()
						exists, err := bot.Storage.IsExistOrder(ctx, ord.OrderID)
						if err != nil {
							bot.Logger.WithError(err).Error("Ошибка получения данных о заказе")
							conn.Close()
							bot.mu.Unlock()
							break
						}

						if exists {
							err = bot.Storage.ChangeOrderStatus(ctx, ord.OrderID, "Filled")
							if err != nil {
								bot.Logger.WithError(err).Error("Ошибка получения данных о заказе")
								conn.Close()
								bot.mu.Unlock()
								break
							}
							err = bot.Storage.CleanOldOrders(ctx)
							if err != nil {
								bot.Logger.WithError(err).Error("Ошибка получения данных о заказе")
								conn.Close()
								bot.mu.Unlock()
								break
							}
							bot.mu.Unlock()
							bot.Logger.Infof("[🔥] Сработал сетка-ордер %s. Перестраиваем уровни.\n", ord.OrderID)
							go bot.CheckAndRefreshGrid(ctx)
						} else {
							bot.mu.Unlock()
						}
					}
				}
			}
		}
	}
}

type Storage interface {
	CleanOldOrders(ctx context.Context) error
	CreateOrder(ctx context.Context, order models.Order) error
	GetCountActiveOrders(ctx context.Context) (int, error)
	IsExistOrder(ctx context.Context, orderID string) (bool, error)
	ChangeOrderStatus(ctx context.Context, orderID, newStatus string) error
}

type APIClient interface {
	DoSignedRequest(method, path, queryString string) ([]byte, error)
	GetKlines(symbol string, limit int) ([]models.Candle, error)
	SignParams(secret, timestamp, apiKey, recvWindow, queryString string) string
}
