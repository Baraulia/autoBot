package repository

import (
	"auto-bot/internal/models"
	"context"
	"database/sql"

	_ "modernc.org/sqlite"
)

type Storage struct {
	DB *sql.DB
}

func NewStorage() (*Storage, error) {
	db, err := sql.Open("sqlite", "bot_data.db")
	if err != nil {
		return nil, err
	}

	// Таблица ордеров сетки
	query := `
	CREATE TABLE IF NOT EXISTS orders (
		order_id TEXT PRIMARY KEY,
		symbol TEXT,
		side TEXT,
		price REAL,
		qty REAL,
		position_idx INTEGER,
		status TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = db.Exec(query)
	if err != nil {
		return nil, err
	}

	return &Storage{DB: db}, nil
}

func (s *Storage) Close() {
	s.DB.Close()
}

// Очистка старых ордеров "New" из локальной базы данных при перезапуске
func (s *Storage) CleanOldOrders(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM orders WHERE status = 'New'")
	if err != nil {
		return err
	}

	return nil
}

// Создание заказа
func (s *Storage) CreateOrder(ctx context.Context, order models.Order) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO orders (order_id, symbol, side, price, qty, position_idx, status) VALUES (?, ?, ?, ?, ?, ?, ?)",
		order.OrderID, order.Symbol, order.Side, order.Price, order.Qty, order.PositionIdx, order.Status)
	if err != nil {
		return err
	}

	return nil
}

// Количество заказов в сетке
func (s *Storage) GetCountActiveOrders(ctx context.Context) (int, error) {
	var activeCount int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE status = 'New'").Scan(&activeCount)
	if err != nil {
		return 0, err
	}

	return activeCount, nil
}

// Количество заказов по идентификатору
func (s *Storage) IsExistOrder(ctx context.Context, orderID string) (bool, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE order_id = ?", orderID).Scan(&count)
	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// Количество заказов по идентификатору
func (s *Storage) ChangeOrderStatus(ctx context.Context, orderID, newStatus string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE orders SET status = ? WHERE order_id = ?", newStatus, orderID)
	if err != nil {
		return err
	}

	return nil
}
