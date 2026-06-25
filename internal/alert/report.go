// Package alert (report.go) — формирование HTML/текстового отчёта по циклу
// (разделы 15.4, 15.5 ТЗ): сводка, передача, корзина, диски, ошибки, пропуски.
package alert

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Disk — состояние диска для отчёта.
type Disk struct {
	Name  string
	Total uint64
	Free  uint64
}

func (d Disk) freePct() float64 { return d.FreePercent() }

// FreePercent — процент свободного места.
func (d Disk) FreePercent() float64 {
	if d.Total == 0 {
		return 0
	}
	return float64(d.Free) / float64(d.Total) * 100
}

// UsedPercent — процент занятого места.
func (d Disk) UsedPercent() float64 {
	if d.Total == 0 {
		return 0
	}
	return float64(d.Total-d.Free) / float64(d.Total) * 100
}

// HumanBytes — человекочитаемый размер (экспорт для сервера).
func HumanBytes(b uint64) string { return humanBytes(b) }

// SkippedItem — пропущенный файл.
type SkippedItem struct {
	Relpath  string
	Reason   string
	Attempts int
}

// ErrorItem — ошибка цикла.
type ErrorItem struct {
	Code    string
	Relpath string
	Message string
}

// CycleReport — данные отчёта по циклу.
type CycleReport struct {
	CycleID         int64
	Status          string
	StartedAt       time.Time
	FinishedAt      time.Time
	DownloadedFiles int64
	DownloadedBytes int64
	ChangedFiles    int64
	TrashedFiles    int64
	PurgedFiles     int64
	SkippedFiles    int64
	Passes          int
	AvgSpeed        float64 // байт/с
	PeakParallel    int
	ServerDisk      Disk
	ClientDisk      Disk
	Skipped         []SkippedItem
	Errors          []ErrorItem
	Notes           []string
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func humanRate(bps float64) string {
	return humanBytes(uint64(bps)) + "/с"
}

func dur(d time.Duration) string {
	d = d.Round(time.Second)
	return d.String()
}

func diskCell(d Disk) string {
	return fmt.Sprintf("%.1f%% свободно (%s / %s)", d.freePct(), humanBytes(d.Free), humanBytes(d.Total))
}

// Subject формирует тему письма (15.5).
func (r CycleReport) Subject() string {
	return fmt.Sprintf("[Backuper] Цикл #%d — %s — скачано %d файлов (%s)",
		r.CycleID, r.Status, r.DownloadedFiles, humanBytes(uint64(r.DownloadedBytes)))
}

func (r CycleReport) layout() string {
	return r.StartedAt.Format("2006-01-02 15:04:05") + " — " + r.FinishedAt.Format("15:04:05") +
		" (" + dur(r.FinishedAt.Sub(r.StartedAt)) + ")"
}

// Text — текстовая версия отчёта (fallback).
func (r CycleReport) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Backuper — отчёт по циклу #%d\n", r.CycleID)
	fmt.Fprintf(&b, "Статус:    %s\n", r.Status)
	fmt.Fprintf(&b, "Период:    %s\n\n", r.layout())
	fmt.Fprintf(&b, "Передача:  скачано %d файлов / изменено %d / %s / %s / проходов %d / пик %d\n",
		r.DownloadedFiles, r.ChangedFiles, humanBytes(uint64(r.DownloadedBytes)), humanRate(r.AvgSpeed), r.Passes, r.PeakParallel)
	fmt.Fprintf(&b, "Корзина:   перенесено %d / удалено по сроку %d\n", r.TrashedFiles, r.PurgedFiles)
	fmt.Fprintf(&b, "Диски:     сервер %s; клиент %s\n", diskCell(r.ServerDisk), diskCell(r.ClientDisk))
	fmt.Fprintf(&b, "Пропущено: %d\n", r.SkippedFiles)
	if len(r.Notes) > 0 {
		b.WriteString("\nСобытия:\n")
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "  - %s\n", n)
		}
	}
	if len(r.Errors) > 0 {
		b.WriteString("\nОшибки (код | путь | сообщение):\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "  %s | %s | %s\n", e.Code, e.Relpath, e.Message)
		}
	}
	if len(r.Skipped) > 0 {
		b.WriteString("\nПропущенные (путь | причина | попыток):\n")
		for _, s := range r.Skipped {
			fmt.Fprintf(&b, "  %s | %s | %d\n", s.Relpath, s.Reason, s.Attempts)
		}
	}
	return b.String()
}

func statusColor(s string) string {
	switch s {
	case "OK":
		return "#2e7d32"
	case "PARTIAL":
		return "#f9a825"
	default:
		return "#c62828"
	}
}

// HTML — HTML-версия отчёта (таблицы, 15.5).
func (r CycleReport) HTML() string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(`<html><body style="font-family:Arial,sans-serif;color:#222;font-size:14px">`)
	fmt.Fprintf(&b, `<h2>Backuper — цикл #%d — <span style="color:%s">%s</span></h2>`,
		r.CycleID, statusColor(r.Status), esc(r.Status))

	row := func(k, v string) string {
		return `<tr><td style="padding:4px 10px;background:#f5f5f5;font-weight:bold">` + k +
			`</td><td style="padding:4px 10px">` + v + `</td></tr>`
	}
	b.WriteString(`<table style="border-collapse:collapse;margin-bottom:14px">`)
	b.WriteString(row("Период", esc(r.layout())))
	b.WriteString(row("Передача", fmt.Sprintf("скачано <b>%d</b> файлов / изменено %d / <b>%s</b> / %s / проходов %d / пик %d",
		r.DownloadedFiles, r.ChangedFiles, humanBytes(uint64(r.DownloadedBytes)), humanRate(r.AvgSpeed), r.Passes, r.PeakParallel)))
	b.WriteString(row("Корзина", fmt.Sprintf("перенесено %d / удалено по сроку %d", r.TrashedFiles, r.PurgedFiles)))
	b.WriteString(row("Диск сервера", esc(diskCell(r.ServerDisk))))
	b.WriteString(row("Диск клиента", esc(diskCell(r.ClientDisk))))
	b.WriteString(row("Пропущено", fmt.Sprintf("%d", r.SkippedFiles)))
	b.WriteString(`</table>`)

	if len(r.Notes) > 0 {
		b.WriteString(`<h3>События</h3><ul>`)
		for _, n := range r.Notes {
			b.WriteString(`<li>` + esc(n) + `</li>`)
		}
		b.WriteString(`</ul>`)
	}

	if len(r.Errors) > 0 {
		b.WriteString(`<h3>Ошибки</h3><table style="border-collapse:collapse" border="1" cellpadding="5">`)
		b.WriteString(`<tr style="background:#fdecea"><th>Код</th><th>Путь</th><th>Сообщение</th></tr>`)
		for _, e := range r.Errors {
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td></tr>`, esc(e.Code), esc(e.Relpath), esc(e.Message))
		}
		b.WriteString(`</table>`)
	}

	if len(r.Skipped) > 0 {
		b.WriteString(`<h3>Пропущенные файлы</h3><table style="border-collapse:collapse" border="1" cellpadding="5">`)
		b.WriteString(`<tr style="background:#fff8e1"><th>Путь</th><th>Причина</th><th>Попыток</th></tr>`)
		for _, s := range r.Skipped {
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%d</td></tr>`, esc(s.Relpath), esc(s.Reason), s.Attempts)
		}
		b.WriteString(`</table>`)
	}

	b.WriteString(`</body></html>`)
	return b.String()
}
