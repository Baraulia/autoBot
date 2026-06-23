package service

import (
	"auto-bot/internal/models"
	"context"
)

type Storage interface {
	CreateOrder(ctx context.Context, order models.Order) error
	GetCountActiveOrders(ctx context.Context) (int, error)
	GetActiveOrders(ctx context.Context) ([]models.Order, error)
	UpdateOrderStopLoss(ctx context.Context, orderID string, newStop float64) error
	ChangeOrderStatus(ctx context.Context, orderID, newStatus string) error
	IsExistOrder(ctx context.Context, orderID string) (bool, error)
	CleanOldOrders(ctx context.Context) error
}

type APIClient interface {
	DoSignedRequest(method, path, queryString string) ([]byte, error)
	GetKlines(symbol string, limit int) ([]models.Candle, error)
	SignParams(secret, timestamp, apiKey, recvWindow, queryString string) string
}