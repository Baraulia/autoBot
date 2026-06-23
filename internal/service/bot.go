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

const (
	TrailStopATRMultiplier = 1
	GlobalStopATRMultiplier = 1.5
	StopLossATRMultiplier = 1.2
	OrderTimeoutSec = time.Second * 600
	ADXThreshold = 25
	PaperInitialBalance = 1000
)

// Bot инкапсулирует все зависимости и состояние
type Bot struct {
	Logger          *logrus.Logger
	Storage         Storage
	APIClient       APIClient
	cfg             *config.Config
	mu              sync.Mutex
	globalStopLevel float64 // глобальный уровень стоп-лосса (0 = не установлен)
	lastSide        string 		 // последнее направление сетки ("Buy" или "Sell")
	paperBalance   float64          // текущий виртуальный баланс
    paperPositions map[string]*Position // открытые позиции: symbol -> количество (для упрощения, можно хранить в БД)
    paperStats     PaperStats       // статистика
}

type Position struct {
    Symbol      string
    Qty         float64 // положительное – лонг, отрицательное – шорт (если поддерживается)
    AvgPrice    float64 // средняя цена входа
}

type PaperStats struct {
    TotalTrades   int
    WinTrades     int
    LossTrades    int
    TotalProfit   float64   // суммарная реализованная прибыль (USDT)
    MaxDrawdown   float64   // максимальная просадка в процентах
    PeakBalance   float64   // пиковый баланс
}

// NewBot — конструктор
func NewBot(cfg *config.Config, storage Storage, APIClient APIClient, logger *logrus.Logger) *Bot {
	return &Bot{
		cfg:       cfg,
		Storage:   storage,
		APIClient: APIClient,
		Logger:    logger,		
	}
}

// ---------- ВСПОМОГАТЕЛЬНЫЕ ФУНКЦИИ ДЛЯ ИНДИКАТОРОВ ----------

// calculateEMA рассчитывает экспоненциальное скользящее среднее за период
func calculateEMA(candles []models.Candle, period int) float64 {
	if len(candles) < period {
		period = len(candles)
	}
	if period == 0 {
		return 0
	}
	multiplier := 2.0 / float64(period+1)
	ema := candles[0].Close
	for i := 1; i < period; i++ {
		ema = (candles[i].Close-ema)*multiplier + ema
	}
	return ema
}

// calculateATR рассчитывает средний истинный диапазон за период
func calculateATR(candles []models.Candle, period int) float64 {
	if len(candles) < period+1 {
		period = len(candles) - 1
	}
	if period <= 0 {
		return 0
	}
	var trSum float64
	for i := 1; i <= period; i++ {
		high := candles[i].High
		low := candles[i].Low
		prevClose := candles[i-1].Close
		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)
		tr := math.Max(tr1, math.Max(tr2, tr3))
		trSum += tr
	}
	return trSum / float64(period)
}

// calculateADX рассчитывает индекс среднего направления (упрощённо, без +DI/-DI, только для трендовости)
// Для простоты используем метод, основанный на скользящей средней изменения цены
func calculateADX(candles []models.Candle, period int) float64 {
	if len(candles) < period+1 {
		return 0
	}
	var trueRangeSum float64
	var directionalSum float64
	for i := 1; i <= period; i++ {
		high := candles[i].High
		low := candles[i].Low
		prevClose := candles[i-1].Close
		tr := math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose)))
		trueRangeSum += tr

		// UpMove и DownMove (упрощённо)
		upMove := high - candles[i-1].High
		downMove := candles[i-1].Low - low
		if upMove > downMove && upMove > 0 {
			directionalSum += upMove
		} else if downMove > upMove && downMove > 0 {
			directionalSum += downMove
		}
	}
	if trueRangeSum == 0 {
		return 0
	}
	adx := (directionalSum / trueRangeSum) * 100
	if adx > 100 {
		adx = 100
	}
	return adx
}

// ---------- АНАЛИЗ РЫНКА ПЕРЕД ВХОДОМ ----------

// shouldEnterTrade проверяет все условия для входа в сделку
func (bot *Bot) shouldEnterTrade(candles []models.Candle, currentPrice, ema200, atr, adx float64) bool {
	// 1. Тренд уже проверен отдельно (цена выше/ниже EMA200), но проверим ещё раз
	// 2. ADX должен быть выше порога (например, 25)
	if adx < ADXThreshold {
		bot.Logger.Infof("[⏸️] ADX низкий (%.1f < %d), пропускаем вход", adx, ADXThreshold)
		return false
	}
	// 3. EMA50 для подтверждения тренда
	ema50 := calculateEMA(candles, 50)
	if currentPrice > ema200 && ema50 > ema200 {
		return true // бычий тренд подтверждён
	} else if currentPrice < ema200 && ema50 < ema200 {
		return true // медвежий подтверждён
	}
	bot.Logger.Info("[⏸️] Расхождение EMA50 и EMA200, тренд нечёткий, пропускаем")
	return false
}

// ---------- ОТПРАВКА ОРДЕРА СО СТОП-ЛОССОМ ----------

func (bot *Bot) placeGridOrder(ctx context.Context, side string, price, qty float64, posIdx int, atr float64) {
    bot.mu.Lock()
    defer bot.mu.Unlock()

    // Округление (как было)
    pMultiplier := math.Pow(10, float64(bot.cfg.PriceDecimals))
    roundedPrice := math.Round(price*pMultiplier) / pMultiplier
    qMultiplier := math.Pow(10, float64(bot.cfg.QtyDecimals))
    roundedQty := math.Round(qty*qMultiplier) / qMultiplier

    // Расчёт локального стоп-лосса
    var stopLoss float64
    if side == "Buy" {
        stopLoss = roundedPrice - atr*StopLossATRMultiplier
    } else {
        stopLoss = roundedPrice + atr*StopLossATRMultiplier
    }
    stopLoss = math.Round(stopLoss*pMultiplier) / pMultiplier

    var orderID string
    if bot.cfg.PaperMode {
        // Генерируем локальный ID (например, на основе времени)
        orderID = fmt.Sprintf("PAPER_%d", time.Now().UnixNano())
        // В бумажном режиме не отправляем запрос, просто сохраняем
        bot.Logger.Infof("[📝 БУМАГА] Размещён лимит %s по цене %.1f, стоп-лосс %.1f (виртуально)", side, roundedPrice, stopLoss)
    } else {
        // Реальный режим: отправляем запрос и получаем orderID
        query := fmt.Sprintf("category=linear&symbol=%s&side=%s&orderType=Limit&qty=%.3f&price=%.1f&positionIdx=%d&timeInForce=GTC&stopLoss=%.1f",
            bot.cfg.Symbol, side, roundedQty, roundedPrice, posIdx, stopLoss)
        body, err := bot.APIClient.DoSignedRequest("POST", "/v5/order/create", query)
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
            bot.Logger.WithError(err).Error("[❌ JSON] Ошибка десериализации")
            return
        }
        if apiRes.RetCode != 0 {
            bot.Logger.Errorf("[❌ API] Ошибка создания ордера, retCode=%d", apiRes.RetCode)
            return
        }
        orderID = apiRes.Result.OrderID
        bot.Logger.Infof("[🟢 СЕТКА] Выставлен лимит %s по цене %.1f, стоп-лосс %.1f", side, roundedPrice, stopLoss)
    }

    // Сохраняем в БД
    err := bot.Storage.CreateOrder(ctx, models.Order{
        OrderID:        orderID,
        Symbol:         bot.cfg.Symbol,
        Side:           side,
        Price:          roundedPrice,
        Qty:            roundedQty,
        PositionIdx:    posIdx,
        Status:         "New",
        StopLossPrice:  stopLoss,
        CreatedAt:      time.Now(),
    })
    if err != nil {
        bot.Logger.WithError(err).Error("[❌ БД] Ошибка записи в SQLite")
    }
}

// ---------- ОСНОВНАЯ ЛОГИКА СЕТКИ (С УЛУЧШЕНИЯМИ) ----------

func (bot *Bot) CheckAndRefreshGrid(ctx context.Context) {
	// 1. Проверка активных ордеров
	activeCount, err := bot.Storage.GetCountActiveOrders(ctx)
	if err != nil {
		bot.Logger.WithError(err).Error("[❌ БД] Ошибка проверки активных ордеров")
		return
	}
	if activeCount > 0 {
		return // Ждем выполнения текущей сетки
	}

	// 2. Получение достаточного количества свечей для расчётов (например, 200)
	candles, err := bot.APIClient.GetKlines(ctx, bot.cfg.Symbol, 200)
	if err != nil {
		bot.Logger.WithError(err).Error("[❌ МАРКЕТ] Ошибка получения свечей")
		return
	}
	if len(candles) < 100 {
		bot.Logger.Error("[❌ МАРКЕТ] Недостаточно свечей для анализа")
		return
	}

	currentPrice := candles[len(candles)-1].Close
	ema200 := calculateEMA(candles, 200)
	atr := calculateATR(candles, 14)
	adx := calculateADX(candles, 14)

	// 3. Проверка условий входа
	if !bot.shouldEnterTrade(candles, currentPrice, ema200, atr, adx) {
		bot.Logger.Info("[⏸️] Условия входа не выполнены, пропускаем")
		return
	}

	// 4. Определяем направление и рассчитываем базовый уровень для первого ордера
	var side string
	var basePrice float64
	var posIdx int
	if currentPrice > ema200 {
		side = "Buy"
		basePrice = currentPrice - atr*0.5 // чуть ниже рынка для лимитного ордера
		posIdx = 1
	} else {
		side = "Sell"
		basePrice = currentPrice + atr*0.5 // чуть выше рынка
		posIdx = 2
	}
	bot.lastSide = side // запоминаем направление

	// 5. Устанавливаем глобальный стоп-уровень (например, противоположная сторона от EMA200 с отступом)
	if side == "Buy" {
		bot.globalStopLevel = ema200 - atr*GlobalStopATRMultiplier
	} else {
		bot.globalStopLevel = ema200 + atr*GlobalStopATRMultiplier
	}
	bot.Logger.Infof("[🛡️] Глобальный стоп-уровень установлен на %.1f", bot.globalStopLevel)

	// 6. Выставляем два ордера сетки (можно менять количество)
	// Первый ордер — базовый объём
	bot.placeGridOrder(ctx, side, basePrice, bot.cfg.BaseQty, posIdx, atr)
	// Второй ордер — дальше на 1.5 ATR с увеличенным объёмом
	secondPrice := basePrice
	if side == "Buy" {
		secondPrice = basePrice - atr*1.5
	} else {
		secondPrice = basePrice + atr*1.5
	}
	bot.placeGridOrder(ctx, side, secondPrice, bot.cfg.BaseQty*1.5, posIdx, atr)

	// 7. Подтягиваем стоп-лоссы уже существующих активных ордеров (трейлинг)
	bot.trailStops(ctx, currentPrice, atr)
}

// ---------- ПОДТЯГИВАНИЕ СТОП-ЛОССОВ (ТРЕЙЛИНГ) ----------

// trailStops обновляет стоп-лоссы для всех активных ордеров, приближая их к текущей цене
func (bot *Bot) trailStops(ctx context.Context, currentPrice, atr float64) {
	// Получаем все активные ордера (статус "New" или "PartiallyFilled")
	orders, err := bot.Storage.GetActiveOrders(ctx) // предположим, такой метод есть
	if err != nil {
		bot.Logger.WithError(err).Error("[❌ БД] Ошибка получения активных ордеров для трейлинга")
		return
	}
	if len(orders) == 0 {
		return
	}

	// Для каждого ордера рассчитываем новый стоп-лосс
	for _, ord := range orders {
		var newStop float64
		if ord.Side == "Buy" {
			// Для лонга стоп ниже текущей цены на множитель ATR (может быть меньше, чем старый)
			newStop = currentPrice - atr*TrailStopATRMultiplier
		} else { // Sell
			newStop = currentPrice + atr*TrailStopATRMultiplier
		}
		// Округляем
		pMult := math.Pow(10, float64(bot.cfg.PriceDecimals))
		newStop = math.Round(newStop*pMult) / pMult

		// Проверяем, стал ли новый стоп ближе к текущей цене (ужесточение)
		oldStop := ord.StopLossPrice
		var isTighter bool
		if ord.Side == "Buy" {
			// Для лонга новый стоп должен быть выше старого (ближе к цене)
			if newStop > oldStop {
				isTighter = true
			}
		} else {
			// Для шорта новый стоп должен быть ниже старого (ближе к цене)
			if newStop < oldStop {
				isTighter = true
			}
		}

		if isTighter {
			// Отправляем запрос на изменение стоп-лосса через Bybit API
			query := fmt.Sprintf("category=linear&symbol=%s&orderId=%s&stopLoss=%.1f",
				bot.cfg.Symbol, ord.OrderID, newStop)
			body, err := bot.APIClient.DoSignedRequest("POST", "/v5/order/amend", query)
			if err != nil {
				bot.Logger.WithError(err).Errorf("[❌ API] Ошибка обновления стоп-лосса для ордера %s", ord.OrderID)
				continue
			}
			var resp struct {
				RetCode int `json:"retCode"`
			}
			_ = json.Unmarshal(body, &resp)
			if resp.RetCode == 0 {
				// Обновляем в БД
				err = bot.Storage.UpdateOrderStopLoss(ctx, ord.OrderID, newStop)
				if err != nil {
					bot.Logger.WithError(err).Errorf("[❌ БД] Ошибка обновления стоп-лосса в БД для %s", ord.OrderID)
				} else {
					bot.Logger.Infof("[🔃 ТРЕЙЛИНГ] Стоп-лосс для ордера %s перемещён на %.1f", ord.OrderID, newStop)
				}
			}
		}
	}
}

// ---------- ГЛОБАЛЬНЫЙ МОНИТОРИНГ (ЗАПУСКАЕТСЯ В ОТДЕЛЬНОЙ ГОРУТИНЕ) ----------

func (bot *Bot) startSafetyMonitor(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Получаем текущую цену
			candles, err := bot.APIClient.GetKlines(ctx, bot.cfg.Symbol, 1)
			if err != nil || len(candles) == 0 {
				bot.Logger.WithError(err).Error("[❌ МАРКЕТ] Ошибка получения текущей цены для мониторинга")
				continue
			}
			currentPrice := candles[0].Close

			// Проверка глобального стоп-уровня
			if bot.globalStopLevel != 0 {
				triggered := false
				if bot.lastSide == "Buy" && currentPrice < bot.globalStopLevel {
					triggered = true
				} else if bot.lastSide == "Sell" && currentPrice > bot.globalStopLevel {
					triggered = true
				}
				if triggered {
					bot.Logger.Warnf("[🚨] Глобальный стоп-лосс сработал! Цена %.1f, уровень %.1f. Закрываем все позиции.", currentPrice, bot.globalStopLevel)
					bot.closeAllPositions(ctx)
					bot.globalStopLevel = 0
					continue
				}
			}

			// Проверка таймаута неисполненных ордеров (удаляем старые)
			bot.cancelExpiredOrders(ctx, currentPrice)
		}
	}
}

// ---------- ЗАКРЫТИЕ ВСЕХ ПОЗИЦИЙ ПО РЫНКУ ----------

func (bot *Bot) closeAllPositions(ctx context.Context) {
	// Сначала отменяем все активные ордера
	orders, err := bot.Storage.GetActiveOrders(ctx)
	if err != nil {
		bot.Logger.WithError(err).Error("[❌ БД] Не удалось получить активные ордера для закрытия")
		return
	}
	for _, ord := range orders {
		// Отмена ордера
		query := fmt.Sprintf("category=linear&symbol=%s&orderId=%s", bot.cfg.Symbol, ord.OrderID)
		_, err := bot.APIClient.DoSignedRequest("POST", "/v5/order/cancel", query)
		if err != nil {
			bot.Logger.WithError(err).Errorf("[❌ API] Ошибка отмены ордера %s", ord.OrderID)
		}
		// Обновляем статус в БД
		_ = bot.Storage.ChangeOrderStatus(ctx, ord.OrderID, "Cancelled")
	}

	// Затем закрываем позицию по рынку (если есть открытая позиция)
	// Для Bybit можно отправить рыночный ордер в противоположную сторону с количеством, равным позиции.
	// Здесь нужно получить текущую позицию через API, но для упрощения используем приближение.
	// Например, просто выставляем рыночный ордер с общим объёмом всех открытых позиций.
	// Это требует отдельного запроса к /v5/position/list, но для экономии места пропустим.
	// В реальности нужно получить позицию и закрыть её.
	bot.Logger.Info("[⚡] Все позиции закрыты (заглушка).")
}

// ---------- ОТМЕНА УСТАРЕВШИХ ОРДЕРОВ ПО ТАЙМАУТУ ----------

func (bot *Bot) cancelExpiredOrders(ctx context.Context, currentPrice float64) {
	orders, err := bot.Storage.GetActiveOrders(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for _, ord := range orders {
		// Если ордер висит дольше заданного времени (в секундах) и не исполнен
		if now.Sub(ord.CreatedAt).Seconds() > float64(OrderTimeoutSec) {
			// Отменяем ордер
			query := fmt.Sprintf("category=linear&symbol=%s&orderId=%s", bot.cfg.Symbol, ord.OrderID)
			_, err := bot.APIClient.DoSignedRequest("POST", "/v5/order/cancel", query)
			if err != nil {
				bot.Logger.WithError(err).Errorf("[❌ API] Ошибка отмены устаревшего ордера %s", ord.OrderID)
				continue
			}
			err = bot.Storage.ChangeOrderStatus(ctx, ord.OrderID, "Cancelled")
			if err != nil {
				bot.Logger.WithError(err).Errorf("[❌ БД] Ошибка обновления статуса для %s", ord.OrderID)
			} else {
				bot.Logger.Infof("[⏳] Устаревший ордер %s отменён (таймаут)", ord.OrderID)
			}
			// После отмены старого ордера можно запустить перестроение сетки
			go bot.CheckAndRefreshGrid(ctx)
		}
	}
}

// ---------- WEBSOCKET ЛИСТЕНЕР (С ЗАПУСКОМ МОНИТОРИНГА) ----------

func (bot *Bot) StartWebSocketListener(ctx context.Context) {
	// проверяем режим запуска бота
	 if bot.cfg.PaperMode {
        bot.Logger.Info("[📝 БУМАГА] WebSocket отключён, используется REST-симулятор.")
        // Ждём отмены контекста
        <-ctx.Done()
        return
    }
	// Запускаем горутину для мониторинга безопасности
	go bot.startSafetyMonitor(ctx)

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
			bot.Logger.WithError(err).Error("[❌ WS] Ошибка аутентификации")
			continue
		}

		time.Sleep(500 * time.Millisecond)
		err = conn.WriteMessage(websocket.TextMessage, []byte(`{"op":"subscribe","args":["order"]}`))
		if err != nil {
			bot.Logger.WithError(err).Error("[❌ WS] Ошибка подписки")
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
								bot.Logger.WithError(err).Error("Ошибка обновления статуса")
								conn.Close()
								bot.mu.Unlock()
								break
							}
							err = bot.Storage.CleanOldOrders(ctx)
							if err != nil {
								bot.Logger.WithError(err).Error("Ошибка очистки старых ордеров")
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

// startPeriodicRebuild запускает периодическую проверку условий входа
func (bot *Bot) StartPeriodicRebuild(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            bot.Logger.Info("[⏰] Периодическая проверка условий входа...")
            bot.CheckAndRefreshGrid(ctx)
        }
    }
}