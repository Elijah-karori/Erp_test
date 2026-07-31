package db

import (
	"errors"
	"sync"
)

// MockOrder represents the order stored in the database
type MockOrder struct {
	OrderID string
	Region  string
	Status  string
}

// Database represents a safe in-memory database mock
type Database struct {
	mu     sync.RWMutex
	orders map[string]*MockOrder
}

// NewDatabase initializes a new mock database with seed data
func NewDatabase() *Database {
	d := &Database{
		orders: make(map[string]*MockOrder),
	}
	// Seed some order data
	d.orders["ORD-EU-1"] = &MockOrder{OrderID: "ORD-EU-1", Region: "EU-West", Status: "PENDING"}
	d.orders["ORD-US-1"] = &MockOrder{OrderID: "ORD-US-1", Region: "US-East", Status: "PENDING"}
	return d
}

// GetOrder retrieves an order by its ID
func (d *Database) GetOrder(orderID string) (*MockOrder, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	order, exists := d.orders[orderID]
	if !exists {
		return nil, errors.New("order not found")
	}

	return order, nil
}

// SaveOrder inserts or updates an order in the database
func (d *Database) SaveOrder(order *MockOrder) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.orders[order.OrderID] = order
}

// UpdateOrderStatus modifies status of an order
func (d *Database) UpdateOrderStatus(orderID, status string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	order, exists := d.orders[orderID]
	if !exists {
		return errors.New("order not found")
	}

	order.Status = status
	return nil
}
