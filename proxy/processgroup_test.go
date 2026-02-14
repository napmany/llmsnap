package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/napmany/llmsnap/proxy/config"
	"github.com/stretchr/testify/assert"
)

var processGroupTestConfig = config.AddDefaultGroupToConfig(config.Config{
	HealthCheckTimeout: 15,
	Models: map[string]config.ModelConfig{
		"model1": getTestSimpleResponderConfig("model1"),
		"model2": getTestSimpleResponderConfig("model2"),
		"model3": getTestSimpleResponderConfig("model3"),
		"model4": getTestSimpleResponderConfig("model4"),
		"model5": getTestSimpleResponderConfig("model5"),
	},
	Groups: map[string]config.GroupConfig{
		"G1": {
			Swap:      true,
			Exclusive: true,
			Members:   []string{"model1", "model2"},
		},
		"G2": {
			Swap:      false,
			Exclusive: true,
			Members:   []string{"model3", "model4"},
		},
	},
})

func TestProcessGroup_DefaultHasCorrectModel(t *testing.T) {
	pg := NewProcessGroup(config.DEFAULT_GROUP_ID, processGroupTestConfig, testLogger, testLogger)
	assert.True(t, pg.HasMember("model5"))
}

func TestProcessGroup_HasMember(t *testing.T) {
	pg := NewProcessGroup("G1", processGroupTestConfig, testLogger, testLogger)
	assert.True(t, pg.HasMember("model1"))
	assert.True(t, pg.HasMember("model2"))
	assert.False(t, pg.HasMember("model3"))
}

// TestProcessGroup_ProxyRequestSwapIsTrueParallel tests that when swap is true
// and multiple requests are made in parallel, only one process is running at a time.
func TestProcessGroup_ProxyRequestSwapIsTrueParallel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	var processGroupTestConfig = config.AddDefaultGroupToConfig(config.Config{
		HealthCheckTimeout: 15,
		Models: map[string]config.ModelConfig{
			// use the same listening so if a model is already running, it will fail
			// this is a way to test that swap isolation is working
			// properly when there are parallel requests made at the
			// same time.
			"model1": getTestSimpleResponderConfigPort("model1", 9832),
			"model2": getTestSimpleResponderConfigPort("model2", 9832),
			"model3": getTestSimpleResponderConfigPort("model3", 9832),
			"model4": getTestSimpleResponderConfigPort("model4", 9832),
			"model5": getTestSimpleResponderConfigPort("model5", 9832),
		},
		Groups: map[string]config.GroupConfig{
			"G1": {
				Swap:    true,
				Members: []string{"model1", "model2", "model3", "model4", "model5"},
			},
		},
	})

	pg := NewProcessGroup("G1", processGroupTestConfig, testLogger, testLogger)
	defer pg.StopProcesses(StopWaitForInflightRequest)

	tests := []string{"model1", "model2", "model3", "model4", "model5"}

	var wg sync.WaitGroup

	wg.Add(len(tests))
	for _, modelName := range tests {
		go func(modelName string) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			w := httptest.NewRecorder()
			assert.NoError(t, pg.ProxyRequest(modelName, w, req))
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), modelName)
		}(modelName)
	}
	wg.Wait()
}

func TestProcessGroup_ProxyRequestSwapIsFalse(t *testing.T) {
	pg := NewProcessGroup("G2", processGroupTestConfig, testLogger, testLogger)
	defer pg.StopProcesses(StopWaitForInflightRequest)

	tests := []string{"model3", "model4"}

	for _, modelName := range tests {
		t.Run(modelName, func(t *testing.T) {
			reqBody := `{"x", "y"}`
			req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(reqBody))
			w := httptest.NewRecorder()
			assert.NoError(t, pg.ProxyRequest(modelName, w, req))
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), modelName)
		})
	}

	// make sure all the processes are running
	for _, process := range pg.processes {
		assert.Equal(t, StateReady, process.CurrentState())
	}
}

func TestProcessGroup_MakeIdleProcesses(t *testing.T) {
	t.Run("sleep-enabled processes are put to sleep", func(t *testing.T) {
		cfg1 := getTestSimpleResponderConfig("idle1")
		cfg1.SleepMode = config.SleepModeEnable
		cfg1.SleepEndpoints = []config.HTTPEndpoint{
			{Endpoint: "/sleep", Method: "POST", Timeout: 5},
		}
		cfg1.WakeEndpoints = []config.HTTPEndpoint{
			{Endpoint: "/wake_up", Method: "POST", Timeout: 5},
		}

		cfg2 := getTestSimpleResponderConfig("idle2")
		cfg2.SleepMode = config.SleepModeEnable
		cfg2.SleepEndpoints = []config.HTTPEndpoint{
			{Endpoint: "/sleep", Method: "POST", Timeout: 5},
		}
		cfg2.WakeEndpoints = []config.HTTPEndpoint{
			{Endpoint: "/wake_up", Method: "POST", Timeout: 5},
		}

		testConfig := config.AddDefaultGroupToConfig(config.Config{
			HealthCheckTimeout: 15,
			Models: map[string]config.ModelConfig{
				"idle1": cfg1,
				"idle2": cfg2,
			},
			Groups: map[string]config.GroupConfig{
				"G1": {
					Swap:    true,
					Members: []string{"idle1", "idle2"},
				},
			},
		})

		pg := NewProcessGroup("G1", testConfig, testLogger, testLogger)
		defer pg.StopProcesses(StopImmediately)

		// Start all processes
		for _, p := range pg.processes {
			assert.NoError(t, p.start())
			assert.Equal(t, StateReady, p.CurrentState())
		}

		pg.MakeIdleProcesses()

		for _, p := range pg.processes {
			assert.Equal(t, StateAsleep, p.CurrentState())
		}
	})

	t.Run("non-sleep processes are stopped", func(t *testing.T) {
		testConfig := config.AddDefaultGroupToConfig(config.Config{
			HealthCheckTimeout: 15,
			Models: map[string]config.ModelConfig{
				"nosleep1": getTestSimpleResponderConfig("nosleep1"),
				"nosleep2": getTestSimpleResponderConfig("nosleep2"),
			},
			Groups: map[string]config.GroupConfig{
				"G1": {
					Swap:    true,
					Members: []string{"nosleep1", "nosleep2"},
				},
			},
		})

		pg := NewProcessGroup("G1", testConfig, testLogger, testLogger)

		// Start all processes
		for _, p := range pg.processes {
			assert.NoError(t, p.start())
			assert.Equal(t, StateReady, p.CurrentState())
		}

		pg.MakeIdleProcesses()

		for _, p := range pg.processes {
			assert.Equal(t, StateStopped, p.CurrentState())
		}
	})

	t.Run("mixed processes", func(t *testing.T) {
		sleepCfg := getTestSimpleResponderConfig("mixed_sleep")
		sleepCfg.SleepMode = config.SleepModeEnable
		sleepCfg.SleepEndpoints = []config.HTTPEndpoint{
			{Endpoint: "/sleep", Method: "POST", Timeout: 5},
		}
		sleepCfg.WakeEndpoints = []config.HTTPEndpoint{
			{Endpoint: "/wake_up", Method: "POST", Timeout: 5},
		}

		testConfig := config.AddDefaultGroupToConfig(config.Config{
			HealthCheckTimeout: 15,
			Models: map[string]config.ModelConfig{
				"mixed_sleep":   sleepCfg,
				"mixed_nosleep": getTestSimpleResponderConfig("mixed_nosleep"),
			},
			Groups: map[string]config.GroupConfig{
				"G1": {
					Swap:    true,
					Members: []string{"mixed_sleep", "mixed_nosleep"},
				},
			},
		})

		pg := NewProcessGroup("G1", testConfig, testLogger, testLogger)
		defer pg.StopProcesses(StopImmediately)

		// Start all processes
		for _, p := range pg.processes {
			assert.NoError(t, p.start())
			assert.Equal(t, StateReady, p.CurrentState())
		}

		pg.MakeIdleProcesses()

		assert.Equal(t, StateAsleep, pg.processes["mixed_sleep"].CurrentState())
		assert.Equal(t, StateStopped, pg.processes["mixed_nosleep"].CurrentState())
	})

	t.Run("empty group", func(t *testing.T) {
		testConfig := config.AddDefaultGroupToConfig(config.Config{
			HealthCheckTimeout: 15,
			Models:             map[string]config.ModelConfig{},
			Groups: map[string]config.GroupConfig{
				"G1": {
					Swap:    true,
					Members: []string{},
				},
			},
		})

		pg := NewProcessGroup("G1", testConfig, testLogger, testLogger)
		// Should not panic
		pg.MakeIdleProcesses()
	})
}
