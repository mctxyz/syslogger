package syslogger

import "github.com/mctxyz/syslogger/internal/logging"

// ErrClosed 表示管理器已关闭，指标出口接口可用 errors.Is 判断。
var ErrClosed = logging.ErrClosed

// Emergency 以原生 severity 0 提交事件，提交失败通过标准日志诊断。
func (c *Component) Emergency(message string, fields map[string]any) {
	c.log(logging.Emergency, message, fields)
}

// Alert 以原生 severity 1 提交需要立即处理的事件。
func (c *Component) Alert(message string, fields map[string]any) {
	c.log(logging.Alert, message, fields)
}

// Critical 以原生 severity 2 提交严重故障事件。
func (c *Component) Critical(message string, fields map[string]any) {
	c.log(logging.Critical, message, fields)
}

// Error 以原生 severity 3 提交错误事件，组件状态由业务单独维护。
func (c *Component) Error(message string, fields map[string]any) {
	c.log(logging.Error, message, fields)
}

// Warning 以原生 severity 4 提交警告事件。
func (c *Component) Warning(message string, fields map[string]any) {
	c.log(logging.Warning, message, fields)
}

// Notice 以原生 severity 5 提交正常但值得关注的事件。
func (c *Component) Notice(message string, fields map[string]any) {
	c.log(logging.Notice, message, fields)
}

// Info 以原生 severity 6 提交一般信息。
func (c *Component) Info(message string, fields map[string]any) {
	c.log(logging.Info, message, fields)
}

// Debug 以原生 severity 7 提交调试事件，等级过滤由设备日志服务配置。
func (c *Component) Debug(message string, fields map[string]any) {
	c.log(logging.Debug, message, fields)
}
