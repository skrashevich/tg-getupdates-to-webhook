package bridge

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"tg-getupdates-to-webhook/internal/config"
)

type runtimeMetrics struct {
	startedAt time.Time

	mu   sync.RWMutex
	bots map[string]*botRuntimeMetrics
}

type botRuntimeMetrics struct {
	offset         int64
	lastUpdateID   int64
	hasLastUpdate  bool
	telegramErrors uint64
	backendErrors  uint64
}

type healthResponse struct {
	Status        string        `json:"status"`
	StartedAt     time.Time     `json:"started_at"`
	UptimeSeconds int64         `json:"uptime_seconds"`
	Metrics       healthMetrics `json:"metrics"`
}

type healthMetrics struct {
	TelegramErrorsTotal uint64             `json:"telegram_errors_total"`
	BackendErrorsTotal  uint64             `json:"backend_errors_total"`
	Bots                []botHealthMetrics `json:"bots"`
}

type botHealthMetrics struct {
	Name           string `json:"name"`
	Offset         int64  `json:"offset"`
	LastUpdateID   *int64 `json:"last_update_id,omitempty"`
	LagByOffset    int64  `json:"lag_by_offset"`
	TelegramErrors uint64 `json:"telegram_errors"`
	BackendErrors  uint64 `json:"backend_errors"`
}

func newRuntimeMetrics(bots []config.BotConfig) *runtimeMetrics {
	metrics := &runtimeMetrics{
		startedAt: time.Now().UTC(),
		bots:      make(map[string]*botRuntimeMetrics, len(bots)),
	}

	for _, bot := range bots {
		name := strings.TrimSpace(bot.Name)
		if name == "" {
			continue
		}
		metrics.bots[name] = &botRuntimeMetrics{}
	}

	return metrics
}

func (metrics *runtimeMetrics) setOffset(botName string, offset int64) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	entry := metrics.getOrCreateBotLocked(botName)
	entry.offset = offset
}

func (metrics *runtimeMetrics) observeUpdate(botName string, updateID int64) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	entry := metrics.getOrCreateBotLocked(botName)
	entry.lastUpdateID = updateID
	entry.hasLastUpdate = true
}

func (metrics *runtimeMetrics) incTelegramError(botName string, _ error) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	entry := metrics.getOrCreateBotLocked(botName)
	entry.telegramErrors++
}

func (metrics *runtimeMetrics) incBackendError(botName string, _ error) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	entry := metrics.getOrCreateBotLocked(botName)
	entry.backendErrors++
}

func (metrics *runtimeMetrics) snapshot() healthResponse {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()

	names := make([]string, 0, len(metrics.bots))
	for name := range metrics.bots {
		names = append(names, name)
	}
	sort.Strings(names)

	bots := make([]botHealthMetrics, 0, len(names))
	var telegramTotal uint64
	var backendTotal uint64

	for _, name := range names {
		entry := metrics.bots[name]
		if entry == nil {
			continue
		}

		telegramTotal += entry.telegramErrors
		backendTotal += entry.backendErrors

		lag := int64(0)
		var lastUpdateID *int64
		if entry.hasLastUpdate {
			id := entry.lastUpdateID
			lastUpdateID = &id

			expectedOffset := id + 1
			if expectedOffset > entry.offset {
				lag = expectedOffset - entry.offset
			}
		}

		bots = append(bots, botHealthMetrics{
			Name:           name,
			Offset:         entry.offset,
			LastUpdateID:   lastUpdateID,
			LagByOffset:    lag,
			TelegramErrors: entry.telegramErrors,
			BackendErrors:  entry.backendErrors,
		})
	}

	status := "ok"
	if telegramTotal > 0 || backendTotal > 0 {
		status = "degraded"
	}

	started := metrics.startedAt.UTC()
	now := time.Now().UTC()
	uptime := now.Sub(started)
	if uptime < 0 {
		uptime = 0
	}

	return healthResponse{
		Status:        status,
		StartedAt:     started,
		UptimeSeconds: int64(uptime.Seconds()),
		Metrics: healthMetrics{
			TelegramErrorsTotal: telegramTotal,
			BackendErrorsTotal:  backendTotal,
			Bots:                bots,
		},
	}
}

func (metrics *runtimeMetrics) getOrCreateBotLocked(botName string) *botRuntimeMetrics {
	name := strings.TrimSpace(botName)
	if name == "" {
		name = "unknown-bot"
	}

	entry := metrics.bots[name]
	if entry != nil {
		return entry
	}

	entry = &botRuntimeMetrics{}
	metrics.bots[name] = entry
	return entry
}

func (service *Service) HealthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", service.handleHealthz)
	return mux
}

func (service *Service) handleHealthz(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		responseWriter.Header().Set("Allow", http.MethodGet)
		http.Error(responseWriter, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(http.StatusOK)

	snapshot := service.metrics.snapshot()
	if err := json.NewEncoder(responseWriter).Encode(snapshot); err != nil {
		service.logger.Error("failed to encode health response", "error", err)
	}
}
