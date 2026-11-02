package telegram

import (
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/oldtyt/frigate-telegram/internal/log"
)

type safeBotLogger struct{ token string }

func (l safeBotLogger) clean(text string) string {
	if l.token == "" {
		return text
	}
	return strings.ReplaceAll(text, l.token, "[REDACTED]")
}
func (l safeBotLogger) Println(values ...interface{}) {
	log.Warn.Print(l.clean(fmt.Sprintln(values...)))
}
func (l safeBotLogger) Printf(format string, values ...interface{}) {
	log.Warn.Print(l.clean(fmt.Sprintf(format, values...)))
}

func ConfigureLogger(token string) { _ = tgbotapi.SetLogger(safeBotLogger{token: token}) }
