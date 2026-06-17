package main

import (
	"auto-bot/config"
	"auto-bot/internal/clients/bybit"
	"auto-bot/internal/repository"
	"auto-bot/internal/service"
	"auto-bot/pkg/logger"
	"context"
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
	storage, err := repository.NewStorage()
	if err != nil {
		lg.WithError(err).Fatal("инициализация хранилища")
	}
	defer storage.Close()

	ByBitClient := bybit.NewByBitClient(cfg)

	// 3. Сборка объекта Bot через конструктор
	bot := service.NewBot(cfg, storage, ByBitClient, lg)

	err = storage.CleanOldOrders(ctx)
	if err != nil {
		lg.WithError(err).Fatal("очистка базы от старых заказов")
	}

	// 4. Запуск фоновых процессов
	go bot.StartWebSocketListener(ctx)
	time.Sleep(2 * time.Second)
	bot.CheckAndRefreshGrid(ctx)
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		bot.CheckAndRefreshGrid(ctx)
	}
}
