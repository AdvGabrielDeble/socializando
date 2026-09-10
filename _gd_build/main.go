package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type previewEnvelope struct {
	Preview Preview
	At      time.Time
}

type appServer struct {
	store    Store
	dataDir  string
	csrf     string
	mu       sync.Mutex
	previews map[string]previewEnvelope
}

func randomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func newAppServer(store Store, dataDir string) *appServer {
	return &appServer{store: store, dataDir: dataDir, csrf: randomToken(), previews: map[string]previewEnvelope{}}
}

func esc(v any) string { return html.EscapeString(fmt.Sprint(v)) }

func (a *appServer) csrfField() string {
	return `<input type="hidden" name="csrf" value="` + esc(a.csrf) + `">`
}

func (a *appServer) checkCSRF(r *http.Request) bool {
	return r.FormValue("csrf") == a.csrf
}

func page(title, body string) string {
	return `<!doctype html><html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + esc(title) + ` — GD Fiscal Saúde</title><style>
:root{--green:#0D4E3A;--gold:#B88B30;--ink:#17211e;--muted:#61706a;--line:#dfe6e2;--bg:#f6f8f7;--danger:#8b1e1e;--ok:#176b48}*{box-sizing:border-box}body{margin:0;font-family:Segoe UI,Arial,sans-serif;background:var(--bg);color:var(--ink)}header{background:#fff;border-bottom:1px solid var(--line);padding:16px 28px;display:flex;gap:28px;align-items:center;position:sticky;top:0;z-index:2}.brand{font-weight:800;color:var(--green);font-size:20px}.brand small{display:block;color:var(--gold);font-size:11px;letter-spacing:.12em}nav{display:flex;gap:9px;flex-wrap:wrap}nav a{color:var(--green);text-decoration:none;font-weight:650;padding:8px 10px;border-radius:8px}nav a:hover{background:#eef5f1}main{max-width:1180px;margin:26px auto;padding:0 22px 42px}.card{background:#fff;border:1px solid var(--line);border-radius:14px;padding:20px;margin:0 0 18px;box-shadow:0 2px 10px rgba(20,60,45,.04)}h1{font-size:26px;margin:0 0 18px;color:var(--green)}h2{font-size:18px;margin:0 0 14px}h3{font-size:15px;margin:18px 0 8px}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:12px}.metric{border-left:4px solid var(--gold);padding:12px 14px;background:#fafbf9;border-radius:8px}.metric b{font-size:23px;display:block;color:var(--green)}label{font-size:13px;font-weight:650;display:block;margin:0 0 5px}input,select{width:100%;padding:10px 11px;border:1px solid #cdd8d2;border-radius:8px;background:#fff;font:inherit}input[type=file]{padding:8px}.row{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px}.row.two{grid-template-columns:repeat(2,minmax(0,1fr))}@media(max-width:800px){.row,.row.two{grid-template-columns:1fr}header{align-items:flex-start;flex-direction:column;gap:8px}}button,.btn{border:0;background:var(--green);color:#fff;border-radius:8px;padding:10px 15px;font-weight:700;cursor:pointer;text-decoration:none;display:inline-block}.btn.secondary,button.secondary{background:#fff;color:var(--green);border:1px solid var(--green)}.btn.gold{background:var(--gold)}table{width:100%;border-collapse:collapse;font-size:13px}th,td{padding:9px 8px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}th{color:var(--green);background:#fafcfb}.scroll{overflow:auto}.msg{padding:12px 14px;border-radius:9px;margin-bottom:16px;background:#eef5f1;color:var(--ok);border:1px solid #cfe2d8}.msg.error{background:#fff0f0;color:var(--danger);border-color:#efcaca}.muted{color:var(--muted);font-size:13px}.pill{display:inline-block;border:1px solid var(--line);border-radius:999px;padding:3px 8px;font-size:11px}.warn{color:#8a5b00}.danger{color:var(--danger)}.actions{display:flex;gap:8px;align-items:end;flex-wrap:wrap}.footer{color:var(--muted);font-size:11px;text-align:center;padding-top:14px}
</style></head><body><header><div class="brand">GD Fiscal Saúde<small>GABRIEL DEBLE</small></div><nav><a href="/">Visão geral</a><a href="/professionals">Profissionais</a><a href="/import">Importar Excel</a><a href="/payments">Escrituração</a><a href="/restrictions">Pendências</a><a href="/export">Exportar</a></nav></header><main>` + body + `<div class="footer">Dados armazenados localmente neste computador • ` + esc(AppVersion) + `</div></main></body></html>`
}

func messageFrom(r *http.Request) string {
	msg := strings.TrimSpace(r.URL.Query().Get("msg"))
	if msg == "" { return "" }
	class := "msg"
	if r.URL.Query().Get("type") == "error" { class += " error" }
	return `<div class="` + class + `">` + esc(msg) + `</div>`
}

func redirectMsg(w http.ResponseWriter, r *http.Request, path, msg, kind string) {
	q := url.Values{"msg": {msg}}
	if kind != "" { q.Set("type", kind) }
	http.Redirect(w, r, path+"?"+q.Encode(), http.StatusSeeOther)
}

func method(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method != want { http.Error(w, "método não permitido", http.StatusMethodNotAllowed); return false }
	return true
}

func (a *appServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK); _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("/", a.dashboard)
	mux.HandleFunc("/professionals", a.professionals)
	mux.HandleFunc("/professionals/new", a.professionalNew)
	mux.HandleFunc("/professionals/edit", a.professionalEdit)
	mux.HandleFunc("/import", a.importPage)
	mux.HandleFunc("/import/preview", a.importPreview)
	mux.HandleFunc("/import/commit", a.importCommit)
	mux.HandleFunc("/payments", a.payments)
	mux.HandleFunc("/payments/validate", a.validatePayments)
	mux.HandleFunc("/restrictions", a.restrictions)
	mux.HandleFunc("/restrictions/resolve", a.resolveRestriction)
	mux.HandleFunc("/export", a.exportPage)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		mux.ServeHTTP(w, r)
	})
}

func (a *appServer) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" { http.NotFound(w, r); return }
	counts, err := a.store.Counts(); if err != nil { http.Error(w, err.Error(), 500); return }
	body := messageFrom(r) + `<h1>GD Fiscal Saúde</h1><div class="grid"><div class="metric"><b>` + strconv.Itoa(counts["professionals"]) + `</b>profissionais</div><div class="metric"><b>` + strconv.Itoa(counts["payments"]) + `</b>lançamentos salvos</div><div class="metric"><b>` + strconv.Itoa(counts["restrictions"]) + `</b>pendências para corrigir</div></div><div class="card" style="margin-top:18px"><h2>Fluxo operacional</h2><p>Cadastre o profissional, importe a planilha Excel, revise/valide os lançamentos e exporte a escrituração por profissional e competência.</p><div class="actions"><a class="btn" href="/professionals">Cadastrar profissional</a><a class="btn gold" href="/import">Importar Excel</a><a class="btn secondary" href="/export">Exportar escrituração</a></div></div>`
	_, _ = io.WriteString(w, page("Visão geral", body))
}

func occupationOptions(selected string) string {
	codes := []string{"225", "226", "230", "231", "232", "255"}
	var b strings.Builder
	for _, code := range codes {
		sel := ""; if code == selected { sel = " selected" }
		fmt.Fprintf(&b, `<option value="%s"%s>%s — %s</option>`, code, sel, code, esc(occupationLabels[code]))
	}
	return b.String()
}

func (a *appServer) professionals(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) { return }
	list, err := a.store.ListProfessionals(); if err != nil { http.Error(w, err.Error(), 500); return }
	var rows strings.Builder
	for _, p := range list { fmt.Fprintf(&rows, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td><a class="btn secondary" href="/professionals/edit?id=%d">Editar</a></td></tr>`, esc(p.Name), esc(p.CPF), esc(occupationLabels[p.OccupationCode]), esc(p.Registry), p.ID) }
	if len(list) == 0 { rows.WriteString(`<tr><td colspan="5" class="muted">Nenhum profissional cadastrado.</td></tr>`) }
	body := messageFrom(r) + `<h1>Profissionais</h1><div class="card"><h2>Novo profissional</h2><form method="post" action="/professionals/new">` + a.csrfField() + `<div class="row"><div><label>Nome completo</label><input name="name" required></div><div><label>CPF</label><input name="cpf" inputmode="numeric" required></div><div><label>Ocupação</label><select name="occupation_code" required>` + occupationOptions("255") + `</select></div><div><label>Registro profissional</label><input name="registry" maxlength="15"></div></div><p><button type="submit">Salvar profissional</button></p></form></div><div class="card"><h2>Profissionais cadastrados</h2><div class="scroll"><table><thead><tr><th>Nome</th><th>CPF</th><th>Ocupação</th><th>Registro</th><th></th></tr></thead><tbody>` + rows.String() + `</tbody></table></div></div>`
	_, _ = io.WriteString(w, page("Profissionais", body))
}

func (a *appServer) professionalNew(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) { return }
	if !a.checkCSRF(r) { http.Error(w, "requisição inválida", 400); return }
	_, err := a.store.AddProfessional(Professional{Name: r.FormValue("name"), CPF: r.FormValue("cpf"), OccupationCode: r.FormValue("occupation_code"), Registry: r.FormValue("registry")})
	if err != nil { redirectMsg(w, r, "/professionals", err.Error(), "error"); return }
	redirectMsg(w, r, "/professionals", "Profissional salvo com sucesso.", "")
}

func (a *appServer) professionalEdit(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.FormValue("id")); if err != nil { http.Error(w, err.Error(), 400); return }
	p, err := a.store.GetProfessional(id); if err != nil { http.Error(w, err.Error(), 404); return }
	if r.Method == http.MethodPost {
		if !a.checkCSRF(r) { http.Error(w, "requisição inválida", 400); return }
		p.Name = r.FormValue("name"); p.CPF = r.FormValue("cpf"); p.OccupationCode = r.FormValue("occupation_code"); p.Registry = r.FormValue("registry")
		if err := a.store.UpdateProfessional(p); err != nil { redirectMsg(w, r, "/professionals/edit?id="+strconv.FormatInt(id, 10), err.Error(), "error"); return }
		redirectMsg(w, r, "/professionals", "Cadastro atualizado.", ""); return
	}
	if r.Method != http.MethodGet { http.Error(w, "método não permitido", 405); return }
	body := `<h1>Editar profissional</h1><div class="card"><form method="post" action="/professionals/edit?id=` + strconv.FormatInt(id, 10) + `">` + a.csrfField() + `<div class="row"><div><label>Nome completo</label><input name="name" value="` + esc(p.Name) + `" required></div><div><label>CPF</label><input name="cpf" value="` + esc(p.CPF) + `" required></div><div><label>Ocupação</label><select name="occupation_code">` + occupationOptions(p.OccupationCode) + `</select></div><div><label>Registro</label><input name="registry" maxlength="15" value="` + esc(p.Registry) + `"></div></div><p><button>Salvar alterações</button> <a class="btn secondary" href="/professionals">Cancelar</a></p></form></div>`
	_, _ = io.WriteString(w, page("Editar profissional", body))
}

func professionalSelect(list []Professional, selected int64) string {
	var b strings.Builder; b.WriteString(`<option value="">Selecione...</option>`)
	for _, p := range list { sel := ""; if p.ID == selected { sel = " selected" }; fmt.Fprintf(&b, `<option value="%d"%s>%s — CPF %s</option>`, p.ID, sel, esc(p.Name), esc(p.CPF)) }
	return b.String()
}

func (a *appServer) importPage(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) { return }
	list, _ := a.store.ListProfessionals()
	body := messageFrom(r) + `<h1>Importar Excel</h1><div class="card"><h2>1. Ler planilha</h2><p class="muted">O sistema detecta automaticamente os blocos mensais e as competências. Linhas com data e valor vazios são tratadas como sem atendimento e não são importadas.</p><form method="post" action="/import/preview" enctype="multipart/form-data">` + a.csrfField() + `<label>Arquivo .xlsx</label><input type="file" name="spreadsheet" accept=".xlsx,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" required><p><button>Gerar prévia</button></p></form></div>`
	if len(list) == 0 { body += `<div class="msg error">Cadastre ao menos um profissional antes de confirmar uma importação.</div>` }
	_, _ = io.WriteString(w, page("Importar Excel", body))
}

func (a *appServer) savePreview(p Preview) string {
	a.mu.Lock(); defer a.mu.Unlock(); now := time.Now()
	for k, v := range a.previews { if now.Sub(v.At) > 30*time.Minute { delete(a.previews, k) } }
	token := randomToken(); a.previews[token] = previewEnvelope{Preview: p, At: now}; return token
}

func (a *appServer) getPreview(token string, consume bool) (Preview, bool) {
	a.mu.Lock(); defer a.mu.Unlock(); v, ok := a.previews[token]
	if !ok || time.Since(v.At) > 30*time.Minute { delete(a.previews, token); return Preview{}, false }
	if consume { delete(a.previews, token) }; return v.Preview, true
}

func (a *appServer) importPreview(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) { return }
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil { redirectMsg(w, r, "/import", "Arquivo inválido ou maior que 32 MB.", "error"); return }
	if !a.checkCSRF(r) { http.Error(w, "requisição inválida", 400); return }
	file, header, err := r.FormFile("spreadsheet"); if err != nil { redirectMsg(w, r, "/import", "Selecione uma planilha .xlsx.", "error"); return }; defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 32<<20)); if err != nil { redirectMsg(w, r, "/import", err.Error(), "error"); return }
	preview, err := parseWorkbookFileName(header.Filename, raw); if err != nil { redirectMsg(w, r, "/import", err.Error(), "error"); return }
	token := a.savePreview(preview); list, _ := a.store.ListProfessionals(); var total int64
	for _, p := range preview.Valid { total += p.AmountCents }
	var summaries strings.Builder; for _, s := range previewCompetenceSummary(preview) { fmt.Fprintf(&summaries, `<tr><td>%02d/%04d</td><td>%d</td><td>%s</td></tr>`, s.Month, s.Year, s.Count, esc(formatMoney(s.Amount))) }
	var restrictions strings.Builder; for i, x := range preview.Restricted { if i >= 12 { break }; fmt.Fprintf(&restrictions, `<tr><td>%s:%d</td><td>%02d/%04d</td><td>%s</td><td class="danger">%s</td></tr>`, esc(x.SourceSheet), x.SourceRow, x.CompetenceMonth, x.CompetenceYear, esc(x.PayerName), esc(x.IssueReason)) }
	body := `<h1>Prévia da importação</h1>`; if len(preview.Errors) > 0 { body += `<div class="msg error">` + esc(strings.Join(preview.Errors, " • ")) + `</div>` }
	body += `<div class="grid"><div class="metric"><b>` + strconv.Itoa(len(preview.Valid)) + `</b>importáveis</div><div class="metric"><b>` + strconv.Itoa(len(preview.Skipped)) + `</b>sem atendimento</div><div class="metric"><b>` + strconv.Itoa(len(preview.Restricted)) + `</b>pendências reais</div><div class="metric"><b>` + esc(formatMoney(total)) + `</b>total importável</div></div><div class="card" style="margin-top:18px"><h2>Competências reconhecidas</h2><div class="scroll"><table><thead><tr><th>Competência</th><th>Lançamentos</th><th>Total</th></tr></thead><tbody>` + summaries.String() + `</tbody></table></div></div>`
	if restrictions.Len() > 0 { body += `<div class="card"><h2>Pendências detectadas</h2><p class="muted">Serão salvas para correção, mas não entrarão na escrituração até serem resolvidas e validadas.</p><div class="scroll"><table><thead><tr><th>Linha</th><th>Competência</th><th>Pagador</th><th>Motivo</th></tr></thead><tbody>` + restrictions.String() + `</tbody></table></div></div>` }
	body += `<div class="card"><h2>2. Confirmar sob o profissional correto</h2><form method="post" action="/import/commit">` + a.csrfField() + `<input type="hidden" name="preview_token" value="` + esc(token) + `"><div class="row two"><div><label>Profissional</label><select name="professional_id" required>` + professionalSelect(list, 0) + `</select></div><div class="actions"><button type="submit">Importar e salvar</button><a class="btn secondary" href="/import">Cancelar</a></div></div></form></div>`
	_, _ = io.WriteString(w, page("Prévia", body))
}

func (a *appServer) importCommit(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) { return }
	if !a.checkCSRF(r) { http.Error(w, "requisição inválida", 400); return }
	professionalID, err := parseID(r.FormValue("professional_id")); if err != nil { redirectMsg(w, r, "/import", "Selecione o profissional.", "error"); return }
	token := r.FormValue("preview_token"); preview, ok := a.getPreview(token, false); if !ok { redirectMsg(w, r, "/import", "Prévia expirada. Gere novamente.", "error"); return }
	stats, err := a.store.ImportPreview(professionalID, preview); if err != nil { redirectMsg(w, r, "/import", err.Error(), "error"); return }
	_, _ = a.getPreview(token, true)
	msg := fmt.Sprintf("Importação concluída: %d lançamentos novos; %d sem atendimento; %d pendências; %d duplicados ignorados.", stats.CreatedPayments, stats.SkippedNoService, stats.CreatedRestrictions, stats.SkippedDuplicates)
	redirectMsg(w, r, "/payments", msg, "")
}

func parseFilter(r *http.Request) PaymentFilter {
	var f PaymentFilter
	if v, err := strconv.ParseInt(r.URL.Query().Get("professional_id"), 10, 64); err == nil && v > 0 { f.ProfessionalID = v }
	if v, err := strconv.Atoi(r.URL.Query().Get("year")); err == nil && v >= 2000 && v <= 2100 { f.Year = v }
	if v, err := strconv.Atoi(r.URL.Query().Get("month")); err == nil && v >= 1 && v <= 12 { f.Month = v }
	f.Status = strings.TrimSpace(r.URL.Query().Get("status")); return f
}

func monthOptions(selected int) string {
	var b strings.Builder; b.WriteString(`<option value="">Todos</option>`)
	for m := 1; m <= 12; m++ { sel := ""; if m == selected { sel = " selected" }; fmt.Fprintf(&b, `<option value="%d"%s>%02d</option>`, m, sel, m) }
	return b.String()
}

func statusOptions(selected string) string {
	items := []string{"", "PENDENTE_REVISAO", "VALIDADO_RECEITA_SAUDE", "EXPORTADO_CSV"}; labels := map[string]string{"": "Todos", "PENDENTE_REVISAO": "Pendente de revisão", "VALIDADO_RECEITA_SAUDE": "Validado Receita Saúde", "EXPORTADO_CSV": "Exportado CSV"}; var b strings.Builder
	for _, v := range items { sel := ""; if v == selected { sel = " selected" }; fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, esc(v), sel, esc(labels[v])) }
	return b.String()
}

func (a *appServer) payments(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) { return }
	f := parseFilter(r); payments, err := a.store.ListPayments(f); if err != nil { http.Error(w, err.Error(), 500); return }; professionals, _ := a.store.ListProfessionals(); var rows strings.Builder; var total int64
	for _, p := range payments { total += p.AmountCents; fmt.Fprintf(&rows, `<tr><td>%s</td><td>%s</td><td>%s<br><span class="muted">%s</span></td><td>%s</td><td>%s</td><td><span class="pill">%s</span></td></tr>`, esc(p.PaymentDate), esc(p.Professional), esc(p.PayerName), esc(p.PayerCPF), esc(p.BeneficiaryName), esc(formatMoney(p.AmountCents)), esc(p.Status)) }
	if len(payments) == 0 { rows.WriteString(`<tr><td colspan="6" class="muted">Nenhum lançamento para os filtros escolhidos.</td></tr>`) }
	yearValue := ""; if f.Year != 0 { yearValue = strconv.Itoa(f.Year) }; validationYear := "2026"; if f.Year != 0 { validationYear = strconv.Itoa(f.Year) }
	body := messageFrom(r) + `<h1>Escrituração</h1><div class="card"><form method="get"><div class="row"><div><label>Profissional</label><select name="professional_id">` + professionalSelect(professionals, f.ProfessionalID) + `</select></div><div><label>Ano</label><input name="year" type="number" min="2000" max="2100" value="` + yearValue + `"></div><div><label>Mês</label><select name="month">` + monthOptions(f.Month) + `</select></div><div><label>Status</label><select name="status">` + statusOptions(f.Status) + `</select></div></div><p><button>Filtrar</button></p></form></div><div class="grid"><div class="metric"><b>` + strconv.Itoa(len(payments)) + `</b>lançamentos exibidos</div><div class="metric"><b>` + esc(formatMoney(total)) + `</b>total exibido</div></div><div class="card" style="margin-top:18px"><div class="scroll"><table><thead><tr><th>Data</th><th>Profissional</th><th>Pagador</th><th>Beneficiário</th><th>Valor</th><th>Status</th></tr></thead><tbody>` + rows.String() + `</tbody></table></div></div><div class="card"><h2>Validar competência para Receita Saúde</h2><form method="post" action="/payments/validate">` + a.csrfField() + `<div class="row"><div><label>Profissional</label><select name="professional_id" required>` + professionalSelect(professionals, f.ProfessionalID) + `</select></div><div><label>Ano</label><input name="year" type="number" min="2000" max="2100" value="` + validationYear + `" required></div><div><label>Mês</label><select name="month" required>` + monthOptions(f.Month) + `</select></div><div class="actions"><button>Validar pendentes</button></div></div></form></div>`
	_, _ = io.WriteString(w, page("Escrituração", body))
}

func (a *appServer) validatePayments(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !a.checkCSRF(r) { return }
	pid, err := parseID(r.FormValue("professional_id")); if err != nil { redirectMsg(w, r, "/payments", err.Error(), "error"); return }; year, _ := strconv.Atoi(r.FormValue("year")); month, _ := strconv.Atoi(r.FormValue("month")); count, err := a.store.ValidatePayments(pid, year, month); if err != nil { redirectMsg(w, r, "/payments", err.Error(), "error"); return }
	redirectMsg(w, r, "/payments", fmt.Sprintf("%d lançamentos validados para Receita Saúde.", count), "")
}

func (a *appServer) restrictions(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) { return }
	list, err := a.store.ListRestrictions(); if err != nil { http.Error(w, err.Error(), 500); return }; body := messageFrom(r) + `<h1>Pendências de importação</h1>`; if len(list) == 0 { body += `<div class="card"><p>Nenhuma pendência aberta.</p></div>` }
	for _, x := range list { body += `<div class="card"><h2>` + esc(x.Professional) + ` — linha ` + strconv.Itoa(x.SourceRow) + `</h2><p class="danger"><b>` + esc(x.IssueCode) + `:</b> ` + esc(x.IssueReason) + `</p><p class="muted">Fonte: ` + esc(x.SourceName) + ` • ` + esc(x.SourceSheet) + ` • competência ` + fmt.Sprintf("%02d/%04d", x.CompetenceMonth, x.CompetenceYear) + `</p><form method="post" action="/restrictions/resolve">` + a.csrfField() + `<input type="hidden" name="id" value="` + strconv.FormatInt(x.ID, 10) + `"><div class="row"><div><label>Nome pagador</label><input name="payer_name" value="` + esc(x.PayerName) + `" required></div><div><label>CPF pagador</label><input name="payer_cpf" value="` + esc(x.PayerCPFRaw) + `" required></div><div><label>Nome beneficiário (vazio = pagador)</label><input name="beneficiary_name" value="` + esc(x.BeneficiaryName) + `"></div><div><label>CPF beneficiário (vazio = pagador)</label><input name="beneficiary_cpf" value="` + esc(x.BeneficiaryCPFRaw) + `"></div></div><div class="row" style="margin-top:12px"><div><label>Data</label><input name="payment_date" value="` + esc(x.PaymentDateRaw) + `" required></div><div><label>Valor</label><input name="amount" value="` + esc(x.AmountRaw) + `" required></div><div class="actions"><button>Corrigir e criar lançamento</button></div></div></form></div>` }
	_, _ = io.WriteString(w, page("Pendências", body))
}

func (a *appServer) resolveRestriction(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !a.checkCSRF(r) { return }
	id, err := parseID(r.FormValue("id")); if err != nil { redirectMsg(w, r, "/restrictions", err.Error(), "error"); return }
	_, err = a.store.ResolveRestriction(id, r.FormValue("payer_name"), r.FormValue("payer_cpf"), r.FormValue("beneficiary_name"), r.FormValue("beneficiary_cpf"), r.FormValue("payment_date"), r.FormValue("amount")); if err != nil { redirectMsg(w, r, "/restrictions", err.Error(), "error"); return }
	redirectMsg(w, r, "/restrictions", "Pendência corrigida. O lançamento foi criado como PENDENTE_REVISAO.", "")
}

func (a *appServer) exportPage(w http.ResponseWriter, r *http.Request) {
	professionals, _ := a.store.ListProfessionals()
	if r.Method == http.MethodGet {
		body := messageFrom(r) + `<h1>Exportar escrituração</h1><div class="card"><p class="muted">A exportação inclui somente lançamentos VALIDADO_RECEITA_SAUDE do profissional e competência selecionados. O CSV sai sem cabeçalho, separado por ponto e vírgula, com 16 campos.</p><form method="post">` + a.csrfField() + `<div class="row"><div><label>Profissional</label><select name="professional_id" required>` + professionalSelect(professionals, 0) + `</select></div><div><label>Ano</label><input type="number" name="year" min="2000" max="2100" value="2026" required></div><div><label>Mês</label><select name="month" required>` + monthOptions(0) + `</select></div><div class="actions"><button>Gerar CSV</button></div></div></form></div>`
		_, _ = io.WriteString(w, page("Exportar", body)); return
	}
	if r.Method != http.MethodPost || !a.checkCSRF(r) { http.Error(w, "requisição inválida", 400); return }
	pid, err := parseID(r.FormValue("professional_id")); if err != nil { redirectMsg(w, r, "/export", err.Error(), "error"); return }; year, _ := strconv.Atoi(r.FormValue("year")); month, _ := strconv.Atoi(r.FormValue("month")); if year < 2000 || month < 1 || month > 12 { redirectMsg(w, r, "/export", "Competência inválida.", "error"); return }
	prof, err := a.store.GetProfessional(pid); if err != nil { redirectMsg(w, r, "/export", err.Error(), "error"); return }
	payments, err := a.store.ListPayments(PaymentFilter{ProfessionalID: pid, Year: year, Month: month, Status: "VALIDADO_RECEITA_SAUDE"}); if err != nil { redirectMsg(w, r, "/export", err.Error(), "error"); return }
	if len(payments) == 0 { redirectMsg(w, r, "/export", "Não há lançamentos validados para este profissional e competência.", "error"); return }
	if len(payments) > 1000 { redirectMsg(w, r, "/export", "A competência excede 1.000 linhas; divida a escrituração antes de exportar.", "error"); return }
	data, err := receitaCSV(prof, payments); if err != nil { redirectMsg(w, r, "/export", err.Error(), "error"); return }
	ids := make([]int64, 0, len(payments)); for _, p := range payments { ids = append(ids, p.ID) }; if err := a.store.MarkExported(ids); err != nil { redirectMsg(w, r, "/export", err.Error(), "error"); return }
	name := fmt.Sprintf("receita_saude_%04d_%02d_%s.csv", year, month, prof.CPF); w.Header().Set("Content-Type", "text/csv; charset=utf-8"); w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`); w.Header().Set("Content-Length", strconv.Itoa(len(data))); _, _ = w.Write(data)
}

func writeStartupError(dataDir string, err error) {
	if err == nil { return }; if dataDir == "" { dataDir = os.TempDir() }; _ = os.MkdirAll(dataDir, 0700); _ = os.WriteFile(filepath.Join(dataDir, "startup-error.txt"), []byte(time.Now().Format(time.RFC3339)+"\n"+err.Error()+"\n"), 0600)
}

func main() {
	proceed, err := preparePlatform(); if err != nil { writeStartupError("", err); return }; if !proceed { return }
	dataDir, err := dataDirectory(); if err != nil { writeStartupError("", err); return }
	store, err := newStore(dataDir); if err != nil { writeStartupError(dataDir, err); return }; defer store.Close()
	app := newAppServer(store, dataDir); server := &http.Server{Addr: "127.0.0.1:" + localPort, Handler: app.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 45 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	go func() { for i := 0; i < 30; i++ { if serverAlive() { openBrowser("http://127.0.0.1:" + localPort + "/"); return }; time.Sleep(150 * time.Millisecond) } }()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) { writeStartupError(dataDir, err) }
}
