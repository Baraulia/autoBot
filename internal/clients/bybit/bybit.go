package bybit

import (
	"auto-bot/config"
	"auto-bot/internal/models"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type ByBitClient struct {
	BaseURL   string
	APIKey    string
	APISecret string
}

func NewByBitClient(cfg *config.Config) *ByBitClient {
	return &ByBitClient{
		BaseURL:   cfg.BaseURL,
		APIKey:    cfg.APIKey,
		APISecret: cfg.APISecret,
	}
}

type BybitResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List [][]string `json:"list"`
	} `json:"result"`
}

// Генерация подписи HMAC-SHA256 под требования Bybit V5
func (c *ByBitClient) SignParams(secret, timestamp, apiKey, recvWindow, queryString string) string {
	val := timestamp + apiKey + recvWindow + queryString
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(val))
	return hex.EncodeToString(h.Sum(nil))
}

// Выполнение подписанного REST-запроса к Bybit с явной передачей доступов
func (c *ByBitClient) DoSignedRequest(method, path, queryString string) ([]byte, error) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"

	var fullURL string
	if method == "GET" && queryString != "" {
		fullURL = c.BaseURL + path + "?" + queryString
	} else {
		fullURL = c.BaseURL + path
	}

	req, err := http.NewRequest(method, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания HTTP-запроса: %w", err)
	}

	// Установка обязательных заголовков Bybit V5
	req.Header.Set("X-BAPI-API-KEY", c.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN", c.SignParams(c.APISecret, timestamp, c.APIKey, recvWindow, queryString))

	// Инициализируем изолированный клиент с таймаутом
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("сетевая ошибка при отправке запроса: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа сервера: %w", err)
	}

	return body, nil
}

// Запрос исторических свечей (Клайнов) с автоматическим разворотом массива хронологии
func (c *ByBitClient) GetKlines(symbol string, limit int) ([]models.Candle, error) {
	path := "/v5/market/kline"
	query := fmt.Sprintf("category=linear&symbol=%s&interval=240&limit=%d", symbol, limit) // 4H свечи

	resp, err := http.Get(c.BaseURL + path + "?" + query)
	if err != nil {
		return nil, fmt.Errorf("не удалось выполнить запрос свечей: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения тела свечей: %w", err)
	}

	var res BybitResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON свечей: %w", err)
	}

	if res.RetCode != 0 {
		return nil, fmt.Errorf("биржа вернула ошибку: %s (код %d)", res.RetMsg, res.RetCode)
	}

	var candles []models.Candle
	// Разворачиваем ответ, так как Bybit отдает массив от новых к старым
	for i := len(res.Result.List) - 1; i >= 0; i-- {
		item := res.Result.List[i]
		high, _ := strconv.ParseFloat(item[2], 64)   // 2 — это High
		low, _ := strconv.ParseFloat(item[3], 64)    // 3 — это Low
		closeP, _ := strconv.ParseFloat(item[4], 64) // 4 — это Close
		candles = append(candles, models.Candle{High: high, Low: low, Close: closeP})
	}
	return candles, nil
}
