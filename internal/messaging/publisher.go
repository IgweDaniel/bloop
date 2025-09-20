package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/igwedaniel/bloop/internal/types"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

// Publisher interface for loose coupling
type Publisher interface {
	Publish(ctx context.Context, event *types.Event) error
	PublishDeposit(ctx context.Context, deposit *types.WalletDeposit) error
	Close() error
}

// RabbitMQPublisher implements Publisher interface
type RabbitMQPublisher struct {
	conn     *amqp.Connection
	channel  *amqp.Channel
	exchange string
	logger   *logrus.Logger
}

// NewRabbitMQPublisher creates a new RabbitMQ publisher
func NewRabbitMQPublisher(url, exchange string, logger *logrus.Logger) (*RabbitMQPublisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	channel, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	// Declare exchange
	err = channel.ExchangeDeclare(
		exchange,
		"topic",
		true,  // durable
		false, // auto-delete
		false, // internal
		false, // no-wait
		nil,   // arguments
	)
	if err != nil {
		channel.Close()
		conn.Close()
		return nil, fmt.Errorf("failed to declare exchange: %w", err)
	}

	publisher := &RabbitMQPublisher{
		conn:     conn,
		channel:  channel,
		exchange: exchange,
		logger:   logger,
	}

	// Handle connection errors
	go publisher.handleConnectionErrors()

	return publisher, nil
}

func (p *RabbitMQPublisher) handleConnectionErrors() {
	notifyClose := make(chan *amqp.Error)
	p.conn.NotifyClose(notifyClose)

	for err := range notifyClose {
		if err != nil {
			p.logger.Errorf("RabbitMQ connection error: %v", err)
			// In production, implement reconnection logic here
		}
	}
}

// Publish publishes a generic event
func (p *RabbitMQPublisher) Publish(ctx context.Context, event *types.Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	routingKey := fmt.Sprintf("%s.%s", event.Type, event.Source)

	err = p.channel.Publish(
		p.exchange,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			Timestamp:    time.Now(),
			MessageId:    fmt.Sprintf("%s-%d", event.Type, time.Now().UnixNano()),
			DeliveryMode: amqp.Persistent,
		},
	)

	if err != nil {
		return fmt.Errorf("failed to publish event: %w", err)
	}

	p.logger.WithFields(logrus.Fields{
		"event_type":  event.Type,
		"routing_key": routingKey,
		"timestamp":   event.Timestamp,
	}).Debug("Event published successfully")

	return nil
}

// PublishDeposit publishes a wallet deposit event
func (p *RabbitMQPublisher) PublishDeposit(ctx context.Context, deposit *types.WalletDeposit) error {
	event := &types.Event{
		Type:      types.EventTypeWalletDeposit,
		Payload:   deposit,
		Timestamp: time.Now(),
		Source:    string(deposit.Network),
	}

	return p.Publish(ctx, event)
}

// Close closes the publisher connection
func (p *RabbitMQPublisher) Close() error {
	if p.channel != nil {
		p.channel.Close()
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// NoOpPublisher is a no-op implementation for testing
type NoOpPublisher struct{}

func (n *NoOpPublisher) Publish(ctx context.Context, event *types.Event) error {
	return nil
}

func (n *NoOpPublisher) PublishDeposit(ctx context.Context, deposit *types.WalletDeposit) error {
	return nil
}

func (n *NoOpPublisher) Close() error {
	return nil
}

// WebhookPublisher sends events via HTTP webhooks (future implementation)
type WebhookPublisher struct {
	endpoints []string
	client    interface{} // HTTP client
	logger    *logrus.Logger
}

// NewWebhookPublisher creates a new webhook publisher
func NewWebhookPublisher(endpoints []string, logger *logrus.Logger) *WebhookPublisher {
	return &WebhookPublisher{
		endpoints: endpoints,
		logger:    logger,
	}
}

func (w *WebhookPublisher) Publish(ctx context.Context, event *types.Event) error {
	// TODO: Implement webhook publishing
	w.logger.Info("Webhook publishing not implemented yet")
	return nil
}

func (w *WebhookPublisher) PublishDeposit(ctx context.Context, deposit *types.WalletDeposit) error {
	// TODO: Implement webhook publishing
	w.logger.Info("Webhook deposit publishing not implemented yet")
	return nil
}

func (w *WebhookPublisher) Close() error {
	return nil
}
