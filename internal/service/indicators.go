package service

import (
	"auto-bot/internal/models"
	"math"
)

// Расчет Exponential Moving Average (EMA) для фильтрации Lock/Unlock
// Расчет Exponential Moving Average (EMA) для фильтрации Lock/Unlock
func calculateEMA(candles []models.Candle, period int) float64 {
	if len(candles) == 0 {
		return 0.0
	}
	if len(candles) < period {
		return candles[len(candles)-1].Close
	}

	k := 2.0 / (float64(period) + 1.0)

	// ИСПРАВЛЕНО: берем Close первой свечи [0] в качестве начального значения EMA
	ema := candles[0].Close

	for i := 1; i < len(candles); i++ {
		ema = (candles[i].Close * k) + (ema * (1.0 - k))
	}
	return ema
}

// Расчет Average True Range (ATR) для динамического шага сетки
func calculateATR(candles []models.Candle, period int) float64 {
	if len(candles) <= period {
		return 0.0
	}
	var trSum float64
	for i := 1; i < len(candles); i++ {
		tr := math.Max(candles[i].High-candles[i].Low, math.Max(math.Abs(candles[i].High-candles[i-1].Close), math.Abs(candles[i].Low-candles[i-1].Close)))
		if i >= len(candles)-period {
			trSum += tr
		}
	}
	return trSum / float64(period)
}

// Поиск уровней дисбаланса (Fair Value Gap) для Смарт-усреднения
func findSmartLevels(candles []models.Candle, currentPrice, atr float64) (buyLevel, sellLevel float64) {
	buyLevel = currentPrice - (atr * 1.2)  // Дефолтная страховка
	sellLevel = currentPrice + (atr * 1.2) // Дефолтная страховка

	for i := len(candles) - 2; i > 1; i-- {
		if candles[i].Low > candles[i-2].High { // Бычий имбаланс (FVG)
			if val := (candles[i].Low + candles[i-2].High) / 2; val < currentPrice {
				buyLevel = val
				break
			}
		}
		if candles[i].High < candles[i-2].Low { // Медвежий имбаланс (FVG)
			if val := (candles[i].High + candles[i-2].Low) / 2; val > currentPrice {
				sellLevel = val
				break
			}
		}
	}
	return buyLevel, sellLevel
}
