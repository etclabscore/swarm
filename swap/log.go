package swap

import (
	l "github.com/ethereum/go-ethereum/log"
)

const (
	// CallDepth is set to 1 in order to influence to reported line number of
	// the log message with 1 skipped stack frame of calling l.Output()
	CallDepth = 1
	//DefaultAction is the default action filter for SwapLogs
	DefaultAction string = "undefined"
)

// Logger wraps the ethereum logger with specific information for swap logging
type Logger struct {
	action      string
	overlayAddr string
	peerID      string
	logger      l.Logger
}

func wrapCtx(sl Logger, ctx ...interface{}) []interface{} {
	for _, elem := range ctx {
		if elem == "action" && len(ctx)%2 == 0 {
			return ctx
		}
	}
	ctx = addSwapAction(sl, ctx...)
	return ctx
}

// Warn is a convenient alias for log.Warn with a defined action context
func (sl Logger) Warn(msg string, ctx ...interface{}) {
	ctx = wrapCtx(sl, ctx...)
	sl.logger.Warn(msg, ctx...)
}

// Error is a convenient alias for log.Error with a defined action context
func (sl Logger) Error(msg string, ctx ...interface{}) {
	ctx = wrapCtx(sl, ctx...)
	sl.logger.Error(msg, ctx...)
}

//Crit is a convenient alias for log.Crit with a defined action context
func (sl Logger) Crit(msg string, ctx ...interface{}) {
	ctx = wrapCtx(sl, ctx...)
	sl.logger.Crit(msg, ctx...)
}

//Info is a convenient alias for log.Info with a defined action context
func (sl Logger) Info(msg string, ctx ...interface{}) {
	ctx = wrapCtx(sl, ctx...)
	sl.logger.Info(msg, ctx...)
}

//Debug is a convenient alias for log.Debug with a defined action context
func (sl Logger) Debug(msg string, ctx ...interface{}) {
	ctx = wrapCtx(sl, ctx...)
	sl.logger.Debug(msg, ctx...)
}

// Trace is a convenient alias for log.Trace with a defined action context
func (sl Logger) Trace(msg string, ctx ...interface{}) {
	ctx = wrapCtx(sl, ctx...)
	sl.logger.Trace(msg, ctx...)
}

// SetLogAction set the current log action prefix
func (sl *Logger) SetLogAction(action string) {
	//Adds default action undefined
	if action == "" {
		sl.action = DefaultAction
		return
	}
	//Todo validate it's a specific action, if not default
	sl.action = action
}

// NewSwapLogger is an alias for log.New
func NewSwapLogger(overlayAddr string) (swapLogger Logger) {
	swapLogger = Logger{
		action:      DefaultAction,
		overlayAddr: overlayAddr,
	}
	ctx := addSwapCtx(swapLogger)
	swapLogger.logger = l.New(ctx...)
	return swapLogger
}

// NewSwapPeerLogger is an alias for log.New
func NewSwapPeerLogger(overlayAddr string, peerID string) (swapLogger Logger) {

	swapLogger = Logger{
		action:      DefaultAction,
		overlayAddr: overlayAddr,
		peerID:      peerID,
	}
	ctx := addSwapCtx(swapLogger)
	swapLogger.logger = l.New(ctx...)
	return swapLogger
}

func addSwapCtx(sl Logger, ctx ...interface{}) []interface{} {
	ctx = append([]interface{}{"base", sl.overlayAddr}, ctx...)
	if sl.peerID != "" {
		ctx = append(ctx, "peer", sl.peerID)
	}
	return ctx
}

func addSwapAction(sl Logger, ctx ...interface{}) []interface{} {
	return append([]interface{}{"swap_action", sl.action}, ctx...)
}

// GetLogger return the underlining logger
func (sl Logger) GetLogger() (logger l.Logger) {
	return sl.logger
}

// GetHandler return the Handler assigned to root
func GetHandler() l.Handler {
	return l.Root().GetHandler()
}