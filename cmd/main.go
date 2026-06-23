package main

import (
	"auto-bot/config"
	api "auto-bot/internal/clients/bybit"
	storage "auto-bot/internal/repository"
	"auto-bot/internal/service"
	"auto-bot/pkg/logger"
	"context"
	"os"
	"os/signal"
	"time"
)

func main() {
	ctx := context.Background()
	lg := logger.NewLogger()
	lg.Info("[🤖] Старт модульного торгового робота...")

	// 1. Явный и контролируемый запуск конфигурации
	cfg, err := config.LoadConfig(".env")
	if err != nil {
		lg.WithError(err).Fatal("[❌ КРИТИЧЕСКАЯ ОШИБКА КОНФИГУРАЦИИ]")
	}

	// 2. Инициализация хранилища и клиента ByBit
	storage, err := storage.NewSQLiteStorage(cfg.DBPAth, lg)
	if err != nil {
		lg.WithError(err).Fatal("инициализация хранилища")
	}
	defer storage.Close()

	ByBitClient := api.NewBybitClient(cfg.APIKey, cfg.APISecret, cfg.BaseURL)

	// 3. Сборка объекта Bot через конструктор
	bot := service.NewBot(cfg, storage, ByBitClient, lg)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Запускаем бумажный симулятор (если включен)
    if cfg.PaperMode {
        go bot.StartPaperSimulator(ctx)
		go bot.StartStatusLogger(ctx)
    } else {
        go bot.StartWebSocketListener(ctx)
    }

    // Первичная установка сетки (в реальном или бумажном режиме – одинакова)
    go bot.CheckAndRefreshGrid(ctx)
	// Запуск периодической проверки 
	go bot.StartPeriodicRebuild(ctx)

    // Ожидание завершения
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, os.Interrupt)
    <-sigCh
    lg.Info("Получен сигнал завершения, остановка...")
    cancel()
    time.Sleep(2 * time.Second) // даём время завершиться горутинам
    if cfg.PaperMode {
        bot.PrintPaperStats()
    }
}
