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
            candles, err := bot.APIClient.GetKlines(ctx, bot.cfg.Symbol, 1)
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
        // ... (покупка: без изменений, только обновление позиции) ...
        cost := ord.Qty * execPrice
        if bot.paperBalance < cost+fee {
            bot.Logger.Warn("[⚠️ БУМАГА] Недостаточно средств для покупки")
            return
        }
        bot.paperBalance -= (cost + fee)

        pos, exists := bot.paperPositions[ord.Symbol]
        if !exists {
            pos = &Position{Symbol: ord.Symbol, Qty: 0, AvgPrice: 0}
            bot.paperPositions[ord.Symbol] = pos
        }
        totalQty := pos.Qty + ord.Qty
        if totalQty != 0 {
            pos.AvgPrice = (pos.Qty*pos.AvgPrice + ord.Qty*execPrice) / totalQty
        }
        pos.Qty = totalQty

        bot.Logger.Infof("[🟢 БУМАГА] Исполнена покупка %s по цене %.2f, кол-во %.3f, средняя %.2f", ord.Symbol, execPrice, ord.Qty, pos.AvgPrice)
    } else { // Sell
        pos, exists := bot.paperPositions[ord.Symbol]
        if !exists || pos.Qty < ord.Qty {
            bot.Logger.Warn("[⚠️ БУМАГА] Недостаточно позиции для продажи")
            return
        }
        // Расчёт прибыли от этой продажи (частичное или полное закрытие)
        sellQty := ord.Qty
        // Прибыль = (цена продажи - средняя цена входа) * количество
        profit := (execPrice - pos.AvgPrice) * sellQty
        // Вычитаем комиссию на продажу
        fee = sellQty * execPrice * feeRate
        revenue := sellQty * execPrice
        bot.paperBalance += (revenue - fee)

        // Обновляем статистику
        bot.paperStats.TotalTrades++
        if profit > 0 {
            bot.paperStats.WinTrades++
        } else {
            bot.paperStats.LossTrades++
        }
        bot.paperStats.TotalProfit += profit

        // Уменьшаем позицию
        pos.Qty -= sellQty
        if pos.Qty == 0 {
            delete(bot.paperPositions, ord.Symbol)
        } else {
            // Средняя цена остаётся прежней при частичной продаже
        }
        bot.Logger.Infof("[🔴 БУМАГА] Исполнена продажа %s по цене %.2f, кол-во %.3f, прибыль %.2f", ord.Symbol, execPrice, sellQty, profit)
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
    var profit float64
    if pos.Qty > 0 { // лонг
        // Закрываем по рыночной цене (стоп-срабатывание)
        proceeds := pos.Qty * currentPrice
        fee = proceeds * feeRate
        profit = (currentPrice - pos.AvgPrice) * pos.Qty
        bot.paperBalance += (proceeds - fee)
        bot.paperStats.TotalTrades++
        if profit > 0 {
            bot.paperStats.WinTrades++
        } else {
            bot.paperStats.LossTrades++
        }
        bot.paperStats.TotalProfit += profit
        delete(bot.paperPositions, ord.Symbol)
        bot.Logger.Warnf("[🛑 БУМАГА] Стоп-лосс сработал! Закрыта позиция %s по цене %.2f, прибыль %.2f", ord.Symbol, currentPrice, profit)
    }
    _ = bot.Storage.ChangeOrderStatus(ctx, ord.OrderID, "Stopped")
}

// closeAllPaperPositions - закрыть все позиции по рыночной цене
func (bot *Bot) closeAllPaperPositions(ctx context.Context, price float64) {
    bot.mu.Lock()
    defer bot.mu.Unlock()
    feeRate := 0.0006
    for _, pos := range bot.paperPositions {
        if pos.Qty == 0 {
            continue
        }
        var fee float64
        var profit float64
        if pos.Qty > 0 { // лонг
            proceeds := pos.Qty * price
            fee = proceeds * feeRate
            profit = (price - pos.AvgPrice) * pos.Qty
            bot.paperBalance += (proceeds - fee)
        } else { // шорт (если поддерживается)
            // ...
        }
        bot.paperStats.TotalTrades++
        if profit > 0 {
            bot.paperStats.WinTrades++
        } else {
            bot.paperStats.LossTrades++
        }
        bot.paperStats.TotalProfit += profit
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

func (bot *Bot) logStatus(ctx context.Context) {
    if bot.cfg.PaperMode {
        bot.mu.Lock()
        defer bot.mu.Unlock()

        candles, err := bot.APIClient.GetKlines(ctx, bot.cfg.Symbol, 1)
        var currentPrice float64
        if err == nil && len(candles) > 0 {
            currentPrice = candles[0].Close
        }
        totalPL := bot.calcPaperPL(currentPrice)
        currentBalance := bot.paperBalance + totalPL

        bot.Logger.Infof("========== 📊 БУМАЖНЫЙ СТАТУС ==========")
        bot.Logger.Infof("Баланс: %.2f USDT (включая нереал. P&L)", currentBalance)
        bot.Logger.Infof("Реализованная прибыль: %.2f USDT", bot.paperStats.TotalProfit)
        bot.Logger.Infof("Сделок: %d (прибыльных: %d, убыточных: %d)", 
            bot.paperStats.TotalTrades, bot.paperStats.WinTrades, bot.paperStats.LossTrades)
        bot.Logger.Infof("Макс. просадка: %.2f%%", bot.paperStats.MaxDrawdown)
        // Можно добавить активные ордера, если нужно
        orders, _ := bot.Storage.GetActiveOrders(ctx)
        bot.Logger.Infof("Активных лимитных ордеров: %d", len(orders))
        bot.Logger.Infof("========================================")
    } else {
        // Для реального режима можно вывести что-то подобное, но там нет статистики
        // Можно добавить количество исполненных ордеров из БД
        filledCount, _ := bot.Storage.GetFilledOrdersCount(ctx)
        bot.Logger.Infof("========== 📊 РЕАЛЬНЫЙ СТАТУС ==========")
        bot.Logger.Infof("Исполненных ордеров: %d", filledCount)
        bot.Logger.Infof("========================================")
    }
}

// startStatusLogger запускает периодическое логирование (раз в час)
func (bot *Bot) StartStatusLogger(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Hour)
    defer ticker.Stop()
    // Сразу выведем статус при запуске
    bot.logStatus(ctx)
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            bot.logStatus(ctx)
        }
    }
}