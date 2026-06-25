// Package alert — менеджер уведомлений (раздел 15 ТЗ): отчёт по циклу и
// немедленные алерты (диск/перезапуск/наложение) с окном агрегации.
package alert

import (
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"backuper/internal/logx"
)

// Manager — менеджер алертов.
type Manager struct {
	log       *logx.Logger
	smtp      SMTPConfig
	loc       *time.Location
	aggWindow time.Duration

	mu            sync.Mutex
	lastImmediate map[string]time.Time
}

// New создаёт менеджер алертов.
func New(log *logx.Logger, smtp SMTPConfig, loc *time.Location, aggWindow time.Duration) *Manager {
	return &Manager{log: log, smtp: smtp, loc: loc, aggWindow: aggWindow, lastImmediate: map[string]time.Time{}}
}

// SendCycleReport отправляет HTML+текст отчёт по циклу. При сбое отправки — ERROR в лог (15.1).
func (m *Manager) SendCycleReport(r CycleReport) error {
	if err := m.sendRaw(r.Subject(), r.Text(), r.HTML()); err != nil {
		m.log.Error("alert", "не удалось отправить отчёт по циклу #%d: %v", r.CycleID, err)
		return err
	}
	m.log.Info("alert", "отправлен отчёт по циклу #%d (%s)", r.CycleID, r.Status)
	return nil
}

// SendImmediate отправляет срочный алерт. Повторные алерты того же типа в пределах
// окна агрегации подавляются (15.3).
func (m *Manager) SendImmediate(typ, title string, lines []string) error {
	m.mu.Lock()
	if last, ok := m.lastImmediate[typ]; ok && time.Since(last) < m.aggWindow {
		m.mu.Unlock()
		m.log.Info("alert", "повторный немедленный алерт %s подавлен в окне агрегации", typ)
		return nil
	}
	m.lastImmediate[typ] = time.Now()
	m.mu.Unlock()

	subject := "[Backuper] " + title
	text := title + "\n\n" + strings.Join(lines, "\n")

	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(`<html><body style="font-family:Arial,sans-serif;font-size:14px">`)
	fmt.Fprintf(&b, `<h2 style="color:#c62828">%s</h2><ul>`, esc(title))
	for _, l := range lines {
		b.WriteString(`<li>` + esc(l) + `</li>`)
	}
	b.WriteString(`</ul><p style="color:#888">` + esc(time.Now().In(m.loc).Format("2006-01-02 15:04:05 MST")) + `</p></body></html>`)

	if err := m.sendRaw(subject, text, b.String()); err != nil {
		m.log.Error("alert", "не удалось отправить немедленный алерт %q: %v", title, err)
		return err
	}
	m.log.Info("alert", "отправлен немедленный алерт: %s", title)
	return nil
}

// TestConnection проверяет доступность SMTP (для dry-run/check-config), отправляя пробное письмо.
func (m *Manager) TestConnection() error {
	return m.sendRaw("[Backuper] Проверка SMTP",
		"Тестовое письмо Backuper. Если вы его получили — SMTP настроен корректно.",
		`<html><body><p>Тестовое письмо <b>Backuper</b>. SMTP настроен корректно.</p></body></html>`)
}
