package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/igwedaniel/bloop/internal/blockchain"
	"github.com/igwedaniel/bloop/internal/storage"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/sirupsen/logrus"
)

// Handlers contains HTTP handlers for the API
type Handlers struct {
	trackerManager *blockchain.TrackerManager
	storage        storage.Storage
	logger         *logrus.Logger
}

// NewHandlers creates new API handlers
func NewHandlers(trackerManager *blockchain.TrackerManager, storage storage.Storage, logger *logrus.Logger) *Handlers {
	return &Handlers{
		trackerManager: trackerManager,
		storage:        storage,
		logger:         logger,
	}
}

// HealthCheck returns the health status of the service
func (h *Handlers) HealthCheck(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"status":  "healthy",
		"service": "bloop-blockchain-monitor",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// GetTrackerStats returns statistics for all trackers
func (h *Handlers) GetTrackerStats(w http.ResponseWriter, r *http.Request) {
	stats := h.trackerManager.GetStats()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		h.logger.Errorf("Failed to encode tracker stats: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// AddWatchedWallet adds a wallet to the watch list
func (h *Handlers) AddWatchedWallet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Network  string `json:"network"`
		Address  string `json:"address"`
		WalletID string `json:"wallet_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.Network == "" || req.Address == "" || req.WalletID == "" {
		http.Error(w, "Missing required fields: network, address, wallet_id", http.StatusBadRequest)
		return
	}

	// Convert network string to BlockchainType
	network := types.BlockchainType(strings.ToUpper(req.Network))
	if !h.trackerManager.IsSupported(network) {
		http.Error(w, "Unsupported network", http.StatusBadRequest)
		return
	}

	// Add wallet to watch list
	if err := h.trackerManager.AddWatchedWallet(r.Context(), network, req.Address, req.WalletID); err != nil {
		h.logger.Errorf("Failed to add watched wallet: %v", err)
		http.Error(w, "Failed to add wallet", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"status":  "success",
		"message": "Wallet added to watch list",
		"data": map[string]string{
			"network":   req.Network,
			"address":   req.Address,
			"wallet_id": req.WalletID,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	h.logger.WithFields(logrus.Fields{
		"network":   req.Network,
		"address":   req.Address,
		"wallet_id": req.WalletID,
	}).Info("Wallet added to watch list")
}

// RemoveWatchedWallet removes a wallet from the watch list
func (h *Handlers) RemoveWatchedWallet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Network string `json:"network"`
		Address string `json:"address"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.Network == "" || req.Address == "" {
		http.Error(w, "Missing required fields: network, address", http.StatusBadRequest)
		return
	}

	// Convert network string to BlockchainType
	network := types.BlockchainType(strings.ToUpper(req.Network))
	if !h.trackerManager.IsSupported(network) {
		http.Error(w, "Unsupported network", http.StatusBadRequest)
		return
	}

	// Remove wallet from watch list
	if err := h.trackerManager.RemoveWatchedWallet(r.Context(), network, req.Address); err != nil {
		h.logger.Errorf("Failed to remove watched wallet: %v", err)
		http.Error(w, "Failed to remove wallet", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"status":  "success",
		"message": "Wallet removed from watch list",
		"data": map[string]string{
			"network": req.Network,
			"address": req.Address,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	h.logger.WithFields(logrus.Fields{
		"network": req.Network,
		"address": req.Address,
	}).Info("Wallet removed from watch list")
}

// GetSupportedNetworks returns the list of supported blockchain networks
func (h *Handlers) GetSupportedNetworks(w http.ResponseWriter, r *http.Request) {
	networks := h.trackerManager.GetSupportedNetworks()

	response := map[string]interface{}{
		"status": "success",
		"data": map[string]interface{}{
			"networks": networks,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// GetWatchedWallets returns all watched wallets for all networks or a specific network
func (h *Handlers) GetWatchedWallets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check for network query parameter
	networkParam := r.URL.Query().Get("network")

	allWallets := make(map[string]map[string]string) // network -> address -> walletID

	if networkParam != "" {
		// Get wallets for specific network
		network := types.BlockchainType(strings.ToUpper(networkParam))
		if !h.trackerManager.IsSupported(network) {
			http.Error(w, "Unsupported network", http.StatusBadRequest)
			return
		}

		_, exists := h.trackerManager.GetTracker(network)
		if !exists {
			http.Error(w, "Tracker not found for network", http.StatusNotFound)
			return
		}

		// Get watched wallets from storage
		wallets, err := h.getWatchedWalletsForNetwork(r.Context(), network)
		if err != nil {
			h.logger.Errorf("Failed to get watched wallets for %s: %v", network, err)
			http.Error(w, "Failed to get watched wallets", http.StatusInternalServerError)
			return
		}

		allWallets[string(network)] = wallets
	} else {
		// Get wallets for all supported networks
		for _, network := range h.trackerManager.GetSupportedNetworks() {
			wallets, err := h.getWatchedWalletsForNetwork(r.Context(), network)
			if err != nil {
				h.logger.Errorf("Failed to get watched wallets for %s: %v", network, err)
				continue // Skip this network but continue with others
			}
			allWallets[string(network)] = wallets
		}
	}

	response := map[string]interface{}{
		"status": "success",
		"data": map[string]interface{}{
			"watched_wallets": allWallets,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Errorf("Failed to encode watched wallets response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// Helper function to get watched wallets for a specific network
func (h *Handlers) getWatchedWalletsForNetwork(ctx context.Context, network types.BlockchainType) (map[string]string, error) {
	return h.storage.GetWatchedWallets(ctx, network)
}
