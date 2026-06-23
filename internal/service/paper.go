package service

import (
	"auto-bot/internal/models"
	"context"
	"time"
)

func (bot *Bot) StartPaperSimulator(ctx context.Context) {
    if !bot.cfg.PaperMode {
        return
    }
    bot.paperBalance = PaperInitialBalance
    bot.paperPositions = make(map[string]*Position)
    bot.paperStats.PeakBalance = bot.paperBalance

    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            bot.PrintPaperStats() // выводим итоговую статистику
            return
        case <-ticker.C:
            candles, err := bot.APIClient.GetKlines(bot.cfg.Symbol, 1)
            if err != nil || len(candles) == 0 {
                bot.Logger.WithError(err).Error("[❌ БУМАГА] Ошибка получения цены")
                continue
            }
            currentPrice := candles[0].Close

            // Проверяем активные ордера
            orders, err := bot.Storage.GetActiveOrders(ctx)
            if err != nil {
                bot.Logger.WithError(err).Error("[❌ БУМАГА] Ошибка получения активных ордеров")
                continue
            }

            for _, ord := range orders {
                // Проверка стоп-лосса
                if ord.StopLossPrice > 0 {
                    if (ord.Side == "Buy" && currentPrice <= ord.StopLossPrice) ||
                       (ord.Side == "Sell" && currentPrice >= ord.StopLossPrice) {
                        bot.closePaperPosition(ctx, ord, currentPrice)
                        continue
                    }
                }
                // Проверка лимитного ордера
                if ord.Side == "Buy" && currentPrice <= ord.Price {
                    bot.executePaperOrder(ctx, ord, currentPrice)
                } else if ord.Side == "Sell" && currentPrice >= ord.Price {
                    bot.executePaperOrder(ctx, ord, currentPrice)
                }
            }

            // Глобальный стоп
            if bot.globalStopLevel != 0 {
                if (bot.lastSide == "Buy" && currentPrice < bot.globalStopLevel) ||
                   (bot.lastSide == "Sell" && currentPrice > bot.globalStopLevel) {
                    bot.closeAllPaperPositions(ctx, currentPrice)
                    bot.globalStopLevel = 0
                }
            }

            // Обновление статистики
            totalPL := bot.calcPaperPL(currentPrice)
            currentBalance := bot.paperBalance + totalPL
            if currentBalance > bot.paperStats.PeakBalance {
                bot.paperStats.PeakBalance = currentBalance
            }
            drawdown := (bot.paperStats.PeakBalance - currentBalance) / bot.paperStats.PeakBalance * 100
            if drawdown > bot.paperStats.MaxDrawdown {
                bot.paperStats.MaxDrawdown = drawdown
            }

            // Логируем каждые 30 секунд (можно реже)
            if time.Now().Second()%30 == 0 {
                bot.Logger.Infof("[📊 БУМАГА] Баланс: %.2f, P&L: %.2f, Позиций: %d, Просадка: %.2f%%",
                    currentBalance, totalPL, len(bot.paperPositions), bot.paperStats.MaxDrawdown)
            }
        }
    }
}

// executePaperOrder - исполнение лимитного ордера (покупка или продажа)
func (bot *Bot) executePaperOrder(ctx context.Context, ord models.Order, execPrice float64) {
    bot.mu.Lock()
    defer bot.mu.Unlock()

    feeRate := 0.0006
    fee := ord.Qty * execPrice * feeRate

    if ord.Side == "Buy" {
        cost := ord.Qty * execPrice
        if bot.paperBalance < cost+fee {
            bot.Logger.Warn("[⚠️ БУМАГА] Недостаточно средств для покупки")
            return
        }
        bot.paperBalance -= (cost + fee)

        // Обновляем позицию (лонг)
        pos, exists := bot.paperPositions[ord.Symbol]
        if !exists {
            pos = &Position{Symbol: ord.Symbol, Qty: 0, AvgPrice: 0}
            bot.paperPositions[ord.Symbol] = pos
        }
        // Новая средняя цена = (старое_количество * старая_цена + новое_количество * цена) / (старое_количество + новое_количество)
        totalQty := pos.Qty + ord.Qty
        if totalQty != 0 {
            pos.AvgPrice = (pos.Qty*pos.AvgPrice + ord.Qty*execPrice) / totalQty
        } else {
            pos.AvgPrice = 0
        }
        pos.Qty = totalQty

        bot.Logger.Infof("[🟢 БУМАГА] Исполнена покупка %s по цене %.2f, кол-во %.3f, средняя %.2f", ord.Symbol, execPrice, ord.Qty, pos.AvgPrice)
    } else { // Sell
        // Для продажи проверяем, что есть позиция
        pos, exists := bot.paperPositions[ord.Symbol]
        if !exists || pos.Qty < ord.Qty {
            bot.Logger.Warn("[⚠️ БУМАГА] Недостаточно позиции для продажи")
            return
        }
        revenue := ord.Qty * execPrice
        fee = revenue * feeRate
        bot.paperBalance += (revenue - fee)

        // Уменьшаем позицию
        pos.Qty -= ord.Qty
        // Если позиция полностью закрыта, удаляем её (или оставляем с Qty=0)
        if pos.Qty == 0 {
            delete(bot.paperPositions, ord.Symbol)
        } else {
            // Средняя цена не меняется при частичной продаже
        }
        bot.Logger.Infof("[🔴 БУМАГА] Исполнена продажа %s по цене %.2f, кол-во %.3f", ord.Symbol, execPrice, ord.Qty)
    }

    _ = bot.Storage.ChangeOrderStatus(ctx, ord.OrderID, "Filled")
    _ = bot.Storage.CleanOldOrders(ctx)
    go bot.CheckAndRefreshGrid(ctx)
}

// closePaperPosition - закрытие позиции по стопу (рыночная цена)
func (bot *Bot) closePaperPosition(ctx context.Context, ord models.Order, currentPrice float64) {
    bot.mu.Lock()
    defer bot.mu.Unlock()

    pos, exists := bot.paperPositions[ord.Symbol]
    if !exists || pos.Qty == 0 {
        return
    }
    feeRate := 0.0006
    var fee float64
    if pos.Qty > 0 { // лонг
        proceeds := pos.Qty * currentPrice
        fee = proceeds * feeRate
        bot.paperBalance += (proceeds - fee)
        bot.paperStats.LossTrades++
    } else { // шорт (если поддерживается) – для полноты
        // В данной стратегии мы не используем шорт-позиции одновременно с лонг, поэтому оставим заглушку
        // Но можно реализовать аналогично
    }
    delete(bot.paperPositions, ord.Symbol)
    bot.paperStats.TotalTrades++
    _ = bot.Storage.ChangeOrderStatus(ctx, ord.OrderID, "Stopped")
    bot.Logger.Warnf("[🛑 БУМАГА] Стоп-лосс сработал! Закрыта позиция %s по цене %.2f", ord.Symbol, currentPrice)
}

// closeAllPaperPositions - закрыть все позиции по рыночной цене
func (bot *Bot) closeAllPaperPositions(_ context.Context, price float64) {
    bot.mu.Lock()
    defer bot.mu.Unlock()
    feeRate := 0.0006
    for _, pos := range bot.paperPositions {
        if pos.Qty == 0 {
            continue
        }
        var fee float64
        if pos.Qty > 0 { // лонг
            proceeds := pos.Qty * price
            fee = proceeds * feeRate
            bot.paperBalance += (proceeds - fee)
        } else { // шорт
            cost := -pos.Qty * price
            fee = cost * feeRate
            bot.paperBalance -= (cost + fee)
        }
        bot.paperStats.TotalTrades++
    }
    bot.paperPositions = make(map[string]*Position)
    bot.Logger.Warn("[🚨 БУМАГА] Все позиции закрыты по глобальному стопу.")
}

// calcPaperPL - расчёт нереализованной прибыли/убытка по открытым позициям
func (bot *Bot) calcPaperPL(currentPrice float64) float64 {
    bot.mu.Lock()
    defer bot.mu.Unlock()
    var totalPL float64
    for _, pos := range bot.paperPositions {
        // Если позиция лонг (Qty > 0)
        pl := (currentPrice - pos.AvgPrice) * pos.Qty
        totalPL += pl
    }
    return totalPL
}

func (bot *Bot) PrintPaperStats() {
    bot.mu.Lock()
    defer bot.mu.Unlock()
    bot.Logger.Infof("========== 📊 БУМАЖНАЯ ТОРГОВЛЯ (ИТОГИ) ==========")
    bot.Logger.Infof("Итоговый баланс: %.2f USDT", bot.paperBalance)
    totalPL := bot.paperBalance - PaperInitialBalance
    bot.Logger.Infof("Общая прибыль/убыток: %.2f USDT (%.2f%%)", totalPL, totalPL/PaperInitialBalance*100)
    bot.Logger.Infof("Количество сделок: %d", bot.paperStats.TotalTrades)
    bot.Logger.Infof("Прибыльных: %d", bot.paperStats.WinTrades)
    bot.Logger.Infof("Убыточных: %d", bot.paperStats.LossTrades)
    bot.Logger.Infof("Максимальная просадка: %.2f%%", bot.paperStats.MaxDrawdown)
    bot.Logger.Infof("==================================================")
}