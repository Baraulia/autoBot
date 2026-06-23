package api

import (
	"auto-bot/internal/models"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	candkesInterval = 5
)

type BybitClient struct {
	apiKey     string
	apiSecret  string
	baseURL    string // например, "https://api.bybit.com"
	httpClient *http.Client
}

func NewBybitClient(apiKey, apiSecret, baseURL string) *BybitClient {
	return &BybitClient{
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SignParams генерирует подпись для запроса (используется для REST и WebSocket)
func (c *BybitClient) SignParams(secret, timestamp, apiKey, recvWindow, queryString string) string {
	// Для GET-запросов queryString уже содержит параметры, для POST – обычно тело (но в нашем случае queryString – это часть URL)
	// Формат подписи Bybit: timestamp + apiKey + recvWindow + queryString
	data := timestamp + apiKey + recvWindow + queryString
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// DoSignedRequest выполняет подписанный запрос к Bybit V5
func (c *BybitClient) DoSignedRequest(ctx context.Context, method, path, queryString string) ([]byte, error) {
	urlStr := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, urlStr, strings.NewReader(queryString))
	if err != nil {
		return nil, err
	}

	// Заголовки
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"
	signature := c.SignParams(c.apiSecret, timestamp, c.apiKey, recvWindow, queryString)

	req.Header.Set("X-BAPI-API-KEY", c.apiKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-SIGN", signature)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Для POST запросов тело отправляем как строку
	if method == "POST" {
		req.Body = io.NopCloser(strings.NewReader(queryString))
	} else {
		// Для GET параметры обычно в URL, но у нас они уже переданы в queryString
		req.URL.RawQuery = queryString
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// GetKlines получает историю свечей (публичный эндпоинт, не требует подписи)
func (c *BybitClient) GetKlines(ctx context.Context, symbol string, limit int) ([]models.Candle, error) {
	// Параметры: category=linear, interval=5 (или из конфига), limit // можно вынести в конфиг
	urlStr := fmt.Sprintf("%s/v5/market/kline?category=linear&symbol=%s&interval=%d&limit=%d",
		c.baseURL, symbol, candkesInterval, limit)

	// Создаём запрос с контекстом
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, err
	}

	// Используем httpClient (он должен быть в структуре)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List [][]string `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.RetCode != 0 {
		return nil, fmt.Errorf("bybit api error: retCode=%d", result.RetCode)
	}

	// Парсим список свечей: [timestamp, open, high, low, close, volume, turnover]
	var candles []models.Candle
	for _, item := range result.Result.List {
		if len(item) < 5 {
			continue
		}
		open, _ := strconv.ParseFloat(item[1], 64)
		high, _ := strconv.ParseFloat(item[2], 64)
		low, _ := strconv.ParseFloat(item[3], 64)
		close, _ := strconv.ParseFloat(item[4], 64)
		candles = append(candles, models.Candle{
			Open:  open,
			High:  high,
			Low:   low,
			Close: close,
		})
	}
	return candles, nil
}
