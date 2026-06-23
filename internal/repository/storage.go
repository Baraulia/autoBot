package storage

import (
	"auto-bot/internal/models"
	"context"
	"database/sql"
	"time"

	"github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

type SQLiteStorage struct {
	db     *sql.DB
	logger *logrus.Logger
}

func NewSQLiteStorage(dbPath string, logger *logrus.Logger) (*SQLiteStorage, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	// Создаём таблицу, если её нет
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS orders (
		order_id TEXT PRIMARY KEY,
		symbol TEXT,
		side TEXT,
		price REAL,
		qty REAL,
		position_idx INTEGER,
		status TEXT,
		stop_loss_price REAL,
		created_at DATETIME
	);
	CREATE INDEX IF NOT EXISTS idx_status ON orders(status);
	`
	_, err = db.Exec(createTableSQL)
	if err != nil {
		return nil, err
	}
	return &SQLiteStorage{db: db, logger: logger}, nil
}

func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// CreateOrder сохраняет новый ордер
func (s *SQLiteStorage) CreateOrder(ctx context.Context, order models.Order) error {
	query := `INSERT INTO orders (order_id, symbol, side, price, qty, position_idx, status, stop_loss_price, created_at)
	          VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query,
		order.OrderID,
		order.Symbol,
		order.Side,
		order.Price,
		order.Qty,
		order.PositionIdx,
		order.Status,
		order.StopLossPrice,
		order.CreatedAt,
	)
	return err
}

// GetCountActiveOrders возвращает количество ордеров со статусом не "Filled" и не "Cancelled"
func (s *SQLiteStorage) GetCountActiveOrders(ctx context.Context) (int, error) {
	query := `SELECT COUNT(*) FROM orders WHERE status NOT IN ('Filled', 'Cancelled')`
	var count int
	err := s.db.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}

// IsExistOrder проверяет наличие ордера по ID
func (s *SQLiteStorage) IsExistOrder(ctx context.Context, orderID string) (bool, error) {
	query := `SELECT COUNT(*) FROM orders WHERE order_id = ?`
	var count int
	err := s.db.QueryRowContext(ctx, query, orderID).Scan(&count)
	return count > 0, err
}

// ChangeOrderStatus обновляет статус ордера
func (s *SQLiteStorage) ChangeOrderStatus(ctx context.Context, orderID, newStatus string) error {
	query := `UPDATE orders SET status = ? WHERE order_id = ?`
	_, err := s.db.ExecContext(ctx, query, newStatus, orderID)
	return err
}

// CleanOldOrders удаляет завершённые ордера старше 7 дней (пример)
func (s *SQLiteStorage) CleanOldOrders(ctx context.Context) error {
	cutoff := time.Now().AddDate(0, 0, -7)
	query := `DELETE FROM orders WHERE status IN ('Filled', 'Cancelled') AND created_at < ?`
	_, err := s.db.ExecContext(ctx, query, cutoff)
	return err
}

// GetActiveOrders возвращает все активные ордера (не Filled и не Cancelled)
func (s *SQLiteStorage) GetActiveOrders(ctx context.Context) ([]models.Order, error) {
	query := `SELECT order_id, symbol, side, price, qty, position_idx, status, stop_loss_price, created_at
	          FROM orders WHERE status NOT IN ('Filled', 'Cancelled')`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []models.Order
	for rows.Next() {
		var o models.Order
		err := rows.Scan(
			&o.OrderID,
			&o.Symbol,
			&o.Side,
			&o.Price,
			&o.Qty,
			&o.PositionIdx,
			&o.Status,
			&o.StopLossPrice,
			&o.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

// UpdateOrderStopLoss обновляет стоп-лосс ордера
func (s *SQLiteStorage) UpdateOrderStopLoss(ctx context.Context, orderID string, newStop float64) error {
	query := `UPDATE orders SET stop_loss_price = ? WHERE order_id = ?`
	_, err := s.db.ExecContext(ctx, query, newStop, orderID)
	return err
}

// GetFilledOrdersCount возвращает количество исполненных ордеров (статус "Filled")
func (s *SQLiteStorage) GetFilledOrdersCount(ctx context.Context) (int, error) {
	query := `SELECT COUNT(*) FROM orders WHERE status = 'Filled'`
	var count int
	err := s.db.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}
