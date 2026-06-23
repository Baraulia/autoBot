package models

import "time"

// Candle описывает базовую японскую свечу для расчетов
type Candle struct {
	Open  float64
	High  float64
	Low   float64
	Close float64
}

// Order представляет запись ордера в локальной СУБД
type Order struct {
	OrderID     string
	Symbol      string
	Side        string
	Price       float64
	Qty         float64
	PositionIdx int
	Status      string
	StopLossPrice  float64
	CreatedAt      time.Time
}
