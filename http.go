package syslogger

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Handler 固定采集集合并返回指标处理器，外部服务器的生命周期由业务管理。
func (m *Manager) Handler() (http.Handler, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return nil, ErrClosed
	}
	if !m.cfg.HTTP.Enabled {
		return nil, fmt.Errorf("syslogger: HTTP 指标未启用")
	}
	m.frozen = true
	return http.HandlerFunc(m.serveMetrics), nil
}

func (m *Manager) serveMetrics(w http.ResponseWriter, request *http.Request) {
	m.mu.RLock()
	closing := m.closing
	m.mu.RUnlock()
	if closing {
		http.Error(w, "syslogger: 管理器已关闭", http.StatusServiceUnavailable)
		return
	}
	data, err := m.metricsBytes()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if request.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// StartHTTP 同步绑定配置地址，成功后后台提供 /metrics；重复启动返回错误。
func (m *Manager) StartHTTP() error {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return ErrClosed
	}
	if !m.cfg.HTTP.Enabled {
		m.mu.Unlock()
		return fmt.Errorf("syslogger: HTTP 指标未启用")
	}
	if m.httpPhase != outletIdle {
		m.mu.Unlock()
		return fmt.Errorf("syslogger: HTTP 出口已启动或正在启动")
	}
	m.frozen = true
	m.httpPhase = outletStarting
	m.starters.Add(1)
	m.mu.Unlock()
	defer m.starters.Done()
	var listener net.Listener
	var err error
	if m.cfg.HTTP.ListenAddr == "" {
		err = fmt.Errorf("syslogger: StartHTTP 需要 ListenAddr")
	} else {
		listener, err = net.Listen("tcp", m.cfg.HTTP.ListenAddr)
	}
	m.mu.Lock()
	if err != nil {
		m.httpPhase = outletIdle
		m.mu.Unlock()
		return err
	}
	if m.closing {
		m.mu.Unlock()
		_ = listener.Close()
		m.mu.Lock()
		m.httpPhase = outletIdle
		m.mu.Unlock()
		return ErrClosed
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", m.serveMetrics)
	server := &http.Server{Addr: listener.Addr().String(), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	m.server = server
	m.httpDone = make(chan struct{})
	m.httpPhase = outletRunning
	go func() {
		defer close(m.httpDone)
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			m.httpReporter.Printf("project=%q instance_id=%q: HTTP 服务退出: %v", m.id.Project, m.id.InstanceID, serveErr)
		}
	}()
	m.mu.Unlock()
	return nil
}
