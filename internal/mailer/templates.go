package mailer

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
	"time"
)

// Users carry no language preference server-side (the UI locale lives in the browser), so
// every email is bilingual: Thai first, then English.

var bangkok = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Bangkok"); err == nil {
		return loc
	}
	return time.FixedZone("ICT", 7*60*60)
}()

func formatDate(t time.Time) string { return t.In(bangkok).Format("02/01/2006 15:04") }

func periodLabel(month, year int) string { return fmt.Sprintf("%02d/%d", month, year) }

// rendered is a template's output before it is addressed to anyone.
type rendered struct {
	Subject string
	Text    string
	HTML    string
}

func (r rendered) to(email, name string) Message {
	return Message{ToEmail: email, ToName: name, Subject: r.Subject, Text: r.Text, HTML: r.HTML}
}

const layoutHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="margin:0;padding:24px;background:#f4f5f7;font-family:'Segoe UI',Tahoma,'Noto Sans Thai',sans-serif;color:#1f2933">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;background:#ffffff;border-radius:12px;padding:28px">
<tr><td>
<p style="margin:0 0 20px;font-weight:700;font-size:18px;color:#3b5bdb">MT-Sense</p>
{{template "body" .}}
<p style="margin:28px 0 0;font-size:12px;color:#7b8794">{{template "footer" .}}</p>
</td></tr></table>
</td></tr></table>
</body></html>`

const buttonHTML = `{{define "button"}}<p style="margin:24px 0"><a href="{{.URL}}" style="display:inline-block;background:#3b5bdb;color:#ffffff;text-decoration:none;padding:12px 20px;border-radius:8px;font-weight:600">{{.Label}}</a></p>{{end}}`

func render(subject, textTmpl, htmlBody, htmlFooter string, data any) (rendered, error) {
	text, err := texttemplate.New("text").Parse(textTmpl)
	if err != nil {
		return rendered{}, err
	}
	html, err := htmltemplate.New("layout").Parse(layoutHTML + buttonHTML +
		`{{define "body"}}` + htmlBody + `{{end}}{{define "footer"}}` + htmlFooter + `{{end}}`)
	if err != nil {
		return rendered{}, err
	}
	var textOut, htmlOut bytes.Buffer
	if err := text.Execute(&textOut, data); err != nil {
		return rendered{}, err
	}
	if err := html.Execute(&htmlOut, data); err != nil {
		return rendered{}, err
	}
	return rendered{Subject: subject, Text: strings.TrimSpace(textOut.String()) + "\n", HTML: htmlOut.String()}, nil
}

type button struct{ URL, Label string }

// --- new survey round ---

type newRoundData struct {
	Name, OrgName, Period, ClosesAt, SurveyURL, SettingsURL string
	Button                                                  button
}

func renderNewRound(baseURL, name, orgName string, month, year int, closesAt time.Time) (rendered, error) {
	d := newRoundData{
		Name: name, OrgName: orgName, Period: periodLabel(month, year), ClosesAt: formatDate(closesAt),
		SurveyURL: baseURL + "/survey", SettingsURL: baseURL + "/settings",
	}
	d.Button = button{URL: d.SurveyURL, Label: "ตอบแบบสำรวจ · Take the survey"}
	return render(
		fmt.Sprintf("แบบสำรวจรอบ %s เปิดแล้ว · The %s survey is open", d.Period, d.Period),
		`สวัสดีคุณ {{.Name}}

แบบสำรวจความพึงพอใจของ {{.OrgName}} รอบ {{.Period}} เปิดแล้ว ปิดรับคำตอบ {{.ClosesAt}} (เวลาไทย)
คำตอบของคุณไม่ถูกผูกกับชื่อหรืออีเมล HR เห็นเฉพาะภาพรวมของกลุ่มตั้งแต่ 5 คนขึ้นไป

Hi {{.Name}},

The {{.OrgName}} satisfaction survey for {{.Period}} is open until {{.ClosesAt}} (Bangkok time).
Your answer is never linked to your name or email; HR only sees aggregates of 5 or more people.

{{.SurveyURL}}

ปิดการแจ้งเตือนนี้ได้ที่ / Turn these reminders off in Settings: {{.SettingsURL}}`,
		`<p style="margin:0 0 12px">สวัสดีคุณ {{.Name}}</p>
<p style="margin:0 0 12px">แบบสำรวจความพึงพอใจของ <strong>{{.OrgName}}</strong> รอบ <strong>{{.Period}}</strong> เปิดแล้ว ปิดรับคำตอบ {{.ClosesAt}} (เวลาไทย)</p>
<p style="margin:0 0 12px;color:#52606d">The {{.OrgName}} satisfaction survey for {{.Period}} is open until {{.ClosesAt}} (Bangkok time).</p>
{{template "button" .Button}}
<p style="margin:0;font-size:13px;color:#52606d">คำตอบของคุณไม่ถูกผูกกับชื่อหรืออีเมล HR เห็นเฉพาะภาพรวมของกลุ่มตั้งแต่ 5 คนขึ้นไป<br>Your answer is never linked to your name or email; HR only sees aggregates of 5 or more people.</p>`,
		`ปิดการแจ้งเตือนนี้ได้ที่ <a href="{{.SettingsURL}}" style="color:#3b5bdb">ตั้งค่า</a> · Turn these reminders off in <a href="{{.SettingsURL}}" style="color:#3b5bdb">Settings</a>`,
		d,
	)
}

// --- dashboard summary ---

// SummaryMetrics is the org-wide snapshot a leader can email. Everything is an aggregate;
// when Suppressed is set (fewer than models.MinGroupSize respondents) no score is shown.
type SummaryMetrics struct {
	ResponseRateAvailable bool
	SentimentAvailable    bool
	Month, Year           int
	Respondents           int64
	Headcount             int64
	ResponseRate          int
	Suppressed            bool
	Satisfaction          float64
	ENPS                  int
	Positive              int
	Neutral               int
	Negative              int
	BurnoutRisk           int
	DepartmentsAtRisk     int
	OpenAlerts            int64
}

type summaryData struct {
	SummaryMetrics
	Name, OrgName, SenderName, Note, Period, DashboardURL string
	SatisfactionText                                      string
	Button                                                button
}

func renderSummary(baseURL, dashboardPath, name, orgName, senderName, note string, m SummaryMetrics) (rendered, error) {
	d := summaryData{
		SummaryMetrics: m, Name: name, OrgName: orgName, SenderName: senderName, Note: note,
		Period: periodLabel(m.Month, m.Year), DashboardURL: baseURL + dashboardPath,
		SatisfactionText: fmt.Sprintf("%.2f", m.Satisfaction),
	}
	d.Button = button{URL: d.DashboardURL, Label: "เปิดแดชบอร์ด · Open dashboard"}
	return render(
		fmt.Sprintf("สรุปผลสำรวจ %s รอบ %s · Survey summary", orgName, d.Period),
		`สวัสดีคุณ {{.Name}} / Hi {{.Name}},

{{.SenderName}} ส่งสรุปผลสำรวจ {{.OrgName}} รอบ {{.Period}} ให้คุณ
{{.SenderName}} shared the {{.OrgName}} survey summary for {{.Period}}.
{{if .Note}}
"{{.Note}}"
{{end}}
ผู้ตอบ / Respondents: {{if .Suppressed}}—{{else}}{{.Respondents}}{{end}}{{if and .ResponseRateAvailable (not .Suppressed)}} / {{.Headcount}} ({{.ResponseRate}}%) · ฐานบัญชี ณ วันสร้างรอบ / accounts at round creation{{else}} · ไม่มีข้อมูลสำหรับแสดงอัตราการตอบ / Response rate unavailable{{end}}
{{if .Suppressed}}ผู้ตอบไม่ถึง 5 คน จึงยังไม่แสดงคะแนน / Fewer than 5 responses, so scores are hidden.
{{else}}ความพึงพอใจเฉลี่ย / Average satisfaction: {{.SatisfactionText}} / 5
eNPS: {{.ENPS}}
ความรู้สึก / Sentiment: {{if .SentimentAvailable}}บวก positive {{.Positive}}% · กลาง neutral {{.Neutral}}% · ลบ negative {{.Negative}}%{{else}}ยังมีข้อมูลไม่เพียงพอสำหรับแสดงผล / Not enough data to display{{end}}
เสี่ยงหมดไฟ / Burnout risk: {{.BurnoutRisk}}% ({{.DepartmentsAtRisk}} แผนก / departments)
การแจ้งเตือนที่ยังเปิดอยู่ / Open alerts: {{.OpenAlerts}}
{{end}}
{{.DashboardURL}}`,
		`<p style="margin:0 0 12px">สวัสดีคุณ {{.Name}}</p>
<p style="margin:0 0 4px"><strong>{{.SenderName}}</strong> ส่งสรุปผลสำรวจ <strong>{{.OrgName}}</strong> รอบ <strong>{{.Period}}</strong> ให้คุณ</p>
<p style="margin:0 0 16px;color:#52606d">{{.SenderName}} shared the {{.OrgName}} survey summary for {{.Period}}.</p>
{{if .Note}}<blockquote style="margin:0 0 16px;padding:10px 14px;border-left:3px solid #3b5bdb;background:#f0f4ff;white-space:pre-line">{{.Note}}</blockquote>{{end}}
<table role="presentation" width="100%" cellpadding="8" cellspacing="0" style="border-collapse:collapse;font-size:14px">
<tr style="border-bottom:1px solid #e4e7eb"><td>ผู้ตอบ · Respondents</td><td align="right"><strong>{{if .Suppressed}}—{{else}}{{.Respondents}}{{end}}</strong>{{if and .ResponseRateAvailable (not .Suppressed)}} / {{.Headcount}} ({{.ResponseRate}}%)<br>ฐานบัญชี ณ วันสร้างรอบ / accounts at round creation{{else}}<br>ไม่มีข้อมูลสำหรับแสดงอัตราการตอบ / Response rate unavailable{{end}}</td></tr>
{{if .Suppressed}}<tr><td colspan="2" style="color:#52606d">ผู้ตอบไม่ถึง 5 คน จึงยังไม่แสดงคะแนน<br>Fewer than 5 responses, so scores are hidden.</td></tr>
{{else}}<tr style="border-bottom:1px solid #e4e7eb"><td>ความพึงพอใจเฉลี่ย · Avg satisfaction</td><td align="right"><strong>{{.SatisfactionText}}</strong> / 5</td></tr>
<tr style="border-bottom:1px solid #e4e7eb"><td>eNPS</td><td align="right"><strong>{{.ENPS}}</strong></td></tr>
<tr style="border-bottom:1px solid #e4e7eb"><td>ความรู้สึก · Sentiment</td><td align="right">{{if .SentimentAvailable}}+{{.Positive}}% · {{.Neutral}}% · −{{.Negative}}%{{else}}ยังมีข้อมูลไม่เพียงพอสำหรับแสดงผล / Not enough data to display{{end}}</td></tr>
<tr style="border-bottom:1px solid #e4e7eb"><td>เสี่ยงหมดไฟ · Burnout risk</td><td align="right"><strong>{{.BurnoutRisk}}%</strong> ({{.DepartmentsAtRisk}})</td></tr>
<tr><td>การแจ้งเตือนที่เปิดอยู่ · Open alerts</td><td align="right"><strong>{{.OpenAlerts}}</strong></td></tr>{{end}}
</table>
{{template "button" .Button}}`,
		`อีเมลนี้มีเฉพาะตัวเลขภาพรวม ไม่มีความเห็นรายบุคคล · This email contains aggregates only, never individual comments.`,
		d,
	)
}

// --- SMTP test ---

func renderTest(name string, sentAt time.Time) (rendered, error) {
	d := struct{ Name, SentAt string }{Name: name, SentAt: formatDate(sentAt)}
	return render(
		"ทดสอบการส่งอีเมลจาก MT-Sense · MT-Sense test email",
		`สวัสดีคุณ {{.Name}}

ถ้าคุณได้รับอีเมลนี้ แปลว่าการตั้งค่า SMTP ของ MT-Sense ใช้งานได้ (ส่งเมื่อ {{.SentAt}})
If you received this, MT-Sense's SMTP settings work (sent {{.SentAt}}).`,
		`<p style="margin:0 0 12px">สวัสดีคุณ {{.Name}}</p>
<p style="margin:0 0 12px">ถ้าคุณได้รับอีเมลนี้ แปลว่าการตั้งค่า SMTP ของ MT-Sense ใช้งานได้</p>
<p style="margin:0;color:#52606d">If you received this, MT-Sense's SMTP settings work.</p>`,
		`ส่งเมื่อ · Sent {{.SentAt}}`,
		d,
	)
}

// --- password reset ---

func renderPasswordReset(name, link string, expiresAt time.Time, requestedByHR bool) (rendered, error) {
	d := struct {
		Name, Link, ExpiresAt string
		ByHR                  bool
		Button                button
	}{Name: name, Link: link, ExpiresAt: formatDate(expiresAt), ByHR: requestedByHR}
	d.Button = button{URL: link, Label: "ตั้งรหัสผ่านใหม่ · Set a new password"}
	return render(
		"ตั้งรหัสผ่านใหม่สำหรับ MT-Sense · Reset your MT-Sense password",
		`สวัสดีคุณ {{.Name}}

{{if .ByHR}}HR ขอให้คุณตั้งรหัสผ่านใหม่สำหรับบัญชี MT-Sense{{else}}มีคำขอตั้งรหัสผ่านใหม่สำหรับบัญชี MT-Sense ของคุณ{{end}}
เปิดลิงก์นี้เพื่อตั้งรหัสผ่านใหม่ ใช้ได้ครั้งเดียวถึง {{.ExpiresAt}} (เวลาไทย):

{{.Link}}

ถ้าคุณไม่ได้ขอ ไม่ต้องทำอะไร รหัสผ่านเดิมยังใช้ได้

Hi {{.Name}},

{{if .ByHR}}HR asked you to set a new password for your MT-Sense account.{{else}}Someone asked to reset your MT-Sense password.{{end}}
Open the link above to choose a new one. It works once, until {{.ExpiresAt}} (Bangkok time).
If you did not ask for this, ignore this email; your current password still works.`,
		`<p style="margin:0 0 12px">สวัสดีคุณ {{.Name}}</p>
<p style="margin:0 0 4px">{{if .ByHR}}HR ขอให้คุณตั้งรหัสผ่านใหม่สำหรับบัญชี MT-Sense{{else}}มีคำขอตั้งรหัสผ่านใหม่สำหรับบัญชี MT-Sense ของคุณ{{end}}</p>
<p style="margin:0 0 12px;color:#52606d">{{if .ByHR}}HR asked you to set a new password for your MT-Sense account.{{else}}Someone asked to reset your MT-Sense password.{{end}}</p>
{{template "button" .Button}}
<p style="margin:0 0 12px;font-size:13px;color:#52606d">ลิงก์ใช้ได้ครั้งเดียวถึง {{.ExpiresAt}} (เวลาไทย) · The link works once, until {{.ExpiresAt}} (Bangkok time).</p>`,
		`ถ้าคุณไม่ได้ขอ ไม่ต้องทำอะไร รหัสผ่านเดิมยังใช้ได้ · If you did not ask for this, ignore this email.`,
		d,
	)
}
