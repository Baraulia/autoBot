package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config содержит все настройки приложения. Никаких глобальных переменных.
type Config struct {
	BaseURL       string
	WsURL         string
	APIKey        string
	APISecret     string
	Symbol        string
	BaseQty       float64
	QtyDecimals   int
	PriceDecimals int
	DBPAth string
	PaperMode bool
}

// LoadConfig читает файл .env и возвращает заполненную структуру конфигурации
func LoadConfig(filepath string) (*Config, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть файл конфигурации %s: %w", filepath, err)
	}
	defer file.Close()

	// Временная мапа для считанных значений
	envMap := make(map[string]string)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			envMap[key] = val
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ошибка при чтении файла конфигурации: %w", err)
	}

	// Валидация наличия обязательных строковых ключей
	requiredKeys := []string{"BYBIT_API_KEY", "BYBIT_API_SECRET", "BYBIT_BASE_URL", "BYBIT_WS_URL", "TRADE_SYMBOL"}
	for _, key := range requiredKeys {
		if envMap[key] == "" {
			return nil, fmt.Errorf("пропущен обязательный параметр в .env: %s", key)
		}
	}

	// Парсинг числовых значений с явной обработкой ошибок
	baseQty, err := strconv.ParseFloat(envMap["TRADE_BASE_QTY"], 64)
	if err != nil {
		return nil, fmt.Errorf("неверный формат TRADE_BASE_QTY: %w", err)
	}

	qtyDecimals, err := strconv.Atoi(envMap["TRADE_QTY_DECIMALS"])
	if err != nil {
		return nil, fmt.Errorf("неверный формат TRADE_QTY_DECIMALS: %w", err)
	}

	priceDecimals, err := strconv.Atoi(envMap["TRADE_PRICE_DECIMALS"])
	if err != nil {
		return nil, fmt.Errorf("неверный формат TRADE_PRICE_DECIMALS: %w", err)
	}
	mode, err := strconv.ParseBool( envMap["PAPER_MODE"])
	if err != nil {
		return nil, fmt.Errorf("неверный формат PAPER_MODE: %w", err)
	}

	cfg := &Config{
		APIKey:        envMap["BYBIT_API_KEY"],
		APISecret:     envMap["BYBIT_API_SECRET"],
		BaseURL:       envMap["BYBIT_BASE_URL"],
		WsURL:         envMap["BYBIT_WS_URL"],
		Symbol:        envMap["TRADE_SYMBOL"],
		BaseQty:       baseQty,
		QtyDecimals:   qtyDecimals,
		PriceDecimals: priceDecimals,
		DBPAth: envMap["DB_PATH"],
		PaperMode: mode, 
	}

	return cfg, nil
}
