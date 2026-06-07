package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/igwedaniel/bloop/internal/config"
	btypes "github.com/igwedaniel/bloop/internal/types"
	"github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
)

type EventEnvelope struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp string          `json:"timestamp"`
	Source    string          `json:"source"`
}

type TestListener struct {
	conn     *amqp091.Connection
	channel  *amqp091.Channel
	exchange string
	logger   *logrus.Logger
}

func NewTestListener(rabbitURL, exchange string, logger *logrus.Logger) (*TestListener, error) {
	conn, err := amqp091.Dial(rabbitURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	channel, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	return &TestListener{
		conn:     conn,
		channel:  channel,
		exchange: exchange,
		logger:   logger,
	}, nil
}

func (tl *TestListener) Start(ctx context.Context) error {
	// Declare the exchange (should match the one used by the tracker)
	err := tl.channel.ExchangeDeclare(
		tl.exchange, // exchange name (matches config.yaml)
		"topic",     // exchange type
		true,        // durable
		false,       // auto-deleted
		false,       // internal
		false,       // no-wait
		nil,         // arguments
	)
	if err != nil {
		return fmt.Errorf("failed to declare exchange: %w", err)
	}

	// Declare a temporary queue for the test listener
	queue, err := tl.channel.QueueDeclare(
		"test_listener_queue", // queue name
		false,                 // durable (temporary queue)
		true,                  // delete when unused
		false,                 // exclusive
		false,                 // no-wait
		nil,                   // arguments
	)
	if err != nil {
		return fmt.Errorf("failed to declare queue: %w", err)
	}

	routingKeys := []string{"wallet.deposit.*", "wallet.withdrawal.*"}
	for _, routingKey := range routingKeys {
		err = tl.channel.QueueBind(
			queue.Name,  // queue name
			routingKey,  // routing key pattern
			tl.exchange, // exchange (matches config.yaml)
			false,       // no-wait
			nil,         // arguments
		)
		if err != nil {
			return fmt.Errorf("failed to bind queue with routing key %s: %w", routingKey, err)
		}
	}

	// Start consuming messages
	msgs, err := tl.channel.Consume(
		queue.Name, // queue
		"",         // consumer
		true,       // auto-ack
		false,      // exclusive
		false,      // no-local
		false,      // no-wait
		nil,        // args
	)
	if err != nil {
		return fmt.Errorf("failed to register consumer: %w", err)
	}

	tl.logger.Info("🎧 Test listener started")
	tl.logger.Infof("📋 Exchange: %s", tl.exchange)
	tl.logger.Info("🔑 Routes: wallet.deposit.*, wallet.withdrawal.*")
	tl.logger.Info("📦 Queue: test_listener_queue")

	go func() {
		for {
			select {
			case msg := <-msgs:
				tl.handleMessage(msg)
			case <-ctx.Done():
				tl.logger.Info("Context cancelled, stopping message consumption")
				return
			}
		}
	}()

	return nil
}

func (tl *TestListener) handleMessage(msg amqp091.Delivery) {
	receivedAt := time.Now()
	tl.logger.WithFields(logrus.Fields{
		"routing_key": msg.RoutingKey,
		"exchange":    msg.Exchange,
		"received_at": receivedAt.Format("2006-01-02 15:04:05.000"),
	}).Info("📨 Received message")

	var env EventEnvelope
	if err := json.Unmarshal(msg.Body, &env); err != nil {
		tl.logger.WithFields(logrus.Fields{
			"error": err.Error(),
			"body":  string(msg.Body),
		}).Error("❌ Failed to parse event envelope")
		return
	}
	switch env.Type {
	case btypes.EventTypeWalletDeposit:
		tl.handleDeposit(env.Payload, receivedAt)
	case btypes.EventTypeWalletWithdraw:
		tl.handleWithdrawal(env.Payload, receivedAt)
	default:
		tl.logger.WithField("type", env.Type).Debug("Ignoring unsupported event")
		return
	}
}

func (tl *TestListener) handleDeposit(payload json.RawMessage, receivedAt time.Time) {
	var depositEvent btypes.WalletDeposit
	if err := json.Unmarshal(payload, &depositEvent); err != nil {
		tl.logger.WithFields(logrus.Fields{
			"error":   err.Error(),
			"payload": string(payload),
		}).Error("❌ Failed to parse deposit payload")
		return
	}

	// Log the deposit event with nice formatting
	tl.logger.WithFields(logrus.Fields{
		"network":       string(depositEvent.Network),
		"currency":      string(depositEvent.Currency),
		"amount":        depositEvent.Amount,
		"address":       depositEvent.WalletAddress,
		"wallet_id":     depositEvent.WalletID,
		"tx_hash":       depositEvent.TxHash,
		"block_number":  depositEvent.BlockNumber,
		"confirmations": depositEvent.Confirmations,
		"tx_timestamp":  depositEvent.Timestamp.Format("2006-01-02 15:04:05"),
		"received_at":   receivedAt.Format("2006-01-02 15:04:05.000"),
	}).Info("💰 DEPOSIT DETECTED!")

	// Additional detailed logging
	fmt.Printf("%s", "\n"+strings.Repeat("=", 80)+"\n")
	fmt.Printf("🚨 DEPOSIT ALERT 🚨\n")
	fmt.Printf("%s\n", strings.Repeat("=", 80))
	fmt.Printf("🌐 Network:      %s\n", string(depositEvent.Network))
	fmt.Printf("💎 Currency:     %s\n", string(depositEvent.Currency))
	fmt.Printf("💰 Amount:       %s\n", depositEvent.Amount)
	fmt.Printf("📍 Address:      %s\n", depositEvent.WalletAddress)
	fmt.Printf("👤 Wallet ID:    %s\n", depositEvent.WalletID)
	fmt.Printf("🔗 Tx Hash:      %s\n", depositEvent.TxHash)
	fmt.Printf("📦 Block:        %d\n", depositEvent.BlockNumber)
	fmt.Printf("✅ Confirmations:%d\n", depositEvent.Confirmations)
	fmt.Printf("⏰ Tx Time:      %s\n", depositEvent.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("📥 Received:     %s\n", receivedAt.Format("2006-01-02 15:04:05.000"))
	// Calculate the time difference between when the tx happened and when we received the event (clamped >= 0)
	timeDiff := receivedAt.Sub(depositEvent.Timestamp)
	if timeDiff < 0 {
		timeDiff = 0
	}
	fmt.Printf("⏳ Delay:        %.3f seconds\n", timeDiff.Seconds())

	if depositEvent.NetworkFee != "" {
		fmt.Printf("💸 Network Fee:  %s\n", depositEvent.NetworkFee)
	}

	fmt.Printf("%s\n\n", strings.Repeat("=", 80))
}

func (tl *TestListener) handleWithdrawal(payload json.RawMessage, receivedAt time.Time) {
	var withdrawalEvent btypes.WalletWithdrawal
	if err := json.Unmarshal(payload, &withdrawalEvent); err != nil {
		tl.logger.WithFields(logrus.Fields{
			"error":   err.Error(),
			"payload": string(payload),
		}).Error("❌ Failed to parse withdrawal payload")
		return
	}

	tl.logger.WithFields(logrus.Fields{
		"network":       string(withdrawalEvent.Network),
		"currency":      string(withdrawalEvent.Currency),
		"amount":        withdrawalEvent.Amount,
		"address":       withdrawalEvent.WalletAddress,
		"to_address":    withdrawalEvent.ToAddress,
		"wallet_id":     withdrawalEvent.WalletID,
		"tx_hash":       withdrawalEvent.TxHash,
		"block_number":  withdrawalEvent.BlockNumber,
		"confirmations": withdrawalEvent.Confirmations,
		"tx_timestamp":  withdrawalEvent.Timestamp.Format("2006-01-02 15:04:05"),
		"received_at":   receivedAt.Format("2006-01-02 15:04:05.000"),
	}).Info("💸 WITHDRAWAL DETECTED!")

	fmt.Printf("%s", "\n"+strings.Repeat("=", 80)+"\n")
	fmt.Printf("🚨 WITHDRAWAL ALERT 🚨\n")
	fmt.Printf("%s\n", strings.Repeat("=", 80))
	fmt.Printf("🌐 Network:      %s\n", string(withdrawalEvent.Network))
	fmt.Printf("💎 Currency:     %s\n", string(withdrawalEvent.Currency))
	fmt.Printf("💰 Amount:       %s\n", withdrawalEvent.Amount)
	fmt.Printf("📍 From Address: %s\n", withdrawalEvent.WalletAddress)
	fmt.Printf("➡️  To Address:   %s\n", withdrawalEvent.ToAddress)
	fmt.Printf("👤 Wallet ID:    %s\n", withdrawalEvent.WalletID)
	fmt.Printf("🔗 Tx Hash:      %s\n", withdrawalEvent.TxHash)
	fmt.Printf("📦 Block:        %d\n", withdrawalEvent.BlockNumber)
	fmt.Printf("✅ Confirmations:%d\n", withdrawalEvent.Confirmations)
	fmt.Printf("⏰ Tx Time:      %s\n", withdrawalEvent.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("📥 Received:     %s\n", receivedAt.Format("2006-01-02 15:04:05.000"))
	timeDiff := receivedAt.Sub(withdrawalEvent.Timestamp)
	if timeDiff < 0 {
		timeDiff = 0
	}
	fmt.Printf("⏳ Delay:        %.3f seconds\n", timeDiff.Seconds())

	if withdrawalEvent.NetworkFee != "" {
		fmt.Printf("💸 Network Fee:  %s\n", withdrawalEvent.NetworkFee)
	}

	fmt.Printf("%s\n\n", strings.Repeat("=", 80))
}

func (tl *TestListener) Close() error {
	if tl.channel != nil {
		tl.channel.Close()
	}
	if tl.conn != nil {
		tl.conn.Close()
	}
	return nil
}

func main() {
	// Set up logger
	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
		ForceColors:   true,
	})
	logger.SetLevel(logrus.InfoLevel)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("❌ Failed to load config: %v", err)
	}

	rabbitURL := cfg.RabbitMQ.URL
	if envRabbitURL := os.Getenv("RABBITMQ_URL"); envRabbitURL != "" {
		rabbitURL = envRabbitURL
	}
	if rabbitURL == "" {
		logger.Fatal("❌ RabbitMQ URL is not configured")
	}
	logger.WithField("rabbitmq_url", rabbitURL).Info("🐰 Connecting to RabbitMQ...")

	// Create test listener
	listener, err := NewTestListener(rabbitURL, cfg.RabbitMQ.Exchange, logger)
	if err != nil {
		logger.Fatalf("❌ Failed to create test listener: %v", err)
	}
	defer listener.Close()

	// Set up context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the listener
	if err := listener.Start(ctx); err != nil {
		logger.Fatalf("❌ Failed to start test listener: %v", err)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	logger.Info("🛑 Received shutdown signal, stopping test listener...")
	cancel()

	// Give some time for graceful shutdown
	time.Sleep(2 * time.Second)
	logger.Info("👋 Test listener stopped")
}
