package telegram

import (
	"bytes"
	stdlog "log"
	"strings"
	"testing"

	"github.com/oldtyt/frigate-telegram/internal/log"
)

func TestTelegramTransportLogsRedactToken(t *testing.T) {
	var buf bytes.Buffer
	old := log.Warn
	log.Warn = stdlog.New(&buf, "", 0)
	defer func() { log.Warn = old }()
	logger := safeBotLogger{token: "123:secret"}
	logger.Println(`Post "https://api.telegram.org/bot123:secret/getUpdates": timeout`)
	logger.Printf("HTTP request %s", "https://api.telegram.org/bot123:secret/sendVideo")
	if strings.Contains(buf.String(), "123:secret") || strings.Count(buf.String(), "[REDACTED]") != 2 {
		t.Fatal("Telegram token was not redacted")
	}
}
