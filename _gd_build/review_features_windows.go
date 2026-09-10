//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	statusPending       = "PENDENTE_REVISAO"
	statusReceitaReady  = "VALIDADO_RECEITA_SAUDE"
	statusInvoiceReady  = "VALIDADO_NOTA_FISCAL"
	statusNotLaunched   = "NAO_LANCADO"
	statusReceitaPosted = "LANCADO_RECEITA_SAUDE"
)

type paymentReviewMeta struct {
	NonLaunchReason   string
	ReceitaConfirmedAt string
}

type reviewStore interface {
	EnsureReviewSchema() error
	ReviewPayment(int64, string, string) error
	ConfirmReceita(int64, int, int) (int, error)
	ReviewMeta(int64) (paymentReviewMeta, error)
}

func asReviewStore(store Store) (reviewStore, error) {
	rs, ok := store.(reviewStore)
	if !ok {
		return nil, errors.New("armazenamento não suporta revisão individual")
	}
	return rs, nil
}

func (s *sqliteStore) EnsureReviewSchema() error {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()

	rows, err := s.db.queryUnlocked("PRAGMA table_info(payments);")
	if err != nil {
		return err
	}
	hasReason := false
	hasConfirmed := false
	for _, row := range rows {
		switch row["name"] {
		case "non_launch_reason":
			hasReason = true
		case "receita_confirmed_at":
			hasConfirmed = true
		}
	}
	if !hasReason {
		if err := s.db.execUnlocked("ALTER TABLE payments ADD COLUMN non_launch_reason TEXT NOT NULL DEFAULT ''; "); err != nil {
			return err
		}
	}
	if !hasConfirmed {
		if err := s.db.execUnlocked("ALTER TABLE payments ADD COLUMN receita_confirmed_at TEXT;"); err != nil {
			return err
		}
	}

	// Na versão anterior, gerar o CSV mudava automaticamente o estado para EXPORTADO_CSV.
	// Isso não comprova lançamento efetivo no Receita Saúde, portanto o upgrade devolve
	// esses itens ao estado editável VALIDADO_RECEITA_SAUDE.
	if err := s.db.execUnlocked("UPDATE payments SET status='VALIDADO_RECEITA_SAUDE', updated_at=CURRENT_TIMESTAMP WHERE status='EXPORTADO_CSV';"); err != nil {
		return err
	}
	return s.db.execUnlocked("PRAGMA user_version=2;")
}

func reviewTargetAllowed(status string) bool {
	switch status {
	case statusPending, statusReceitaReady, statusInvoiceReady, statusNotLaunched:
		return true
	default:
		return false
	}
}

func (s *sqliteStore) ReviewPayment(id int64, status, reason string) error {
	if id <= 0 {
		return errors.New("lançamento inválido")
	}
	status = strings.TrimSpace(status)
	if !reviewTargetAllowed(status) {
		return errors.New("opção de revisão inválida")
	}
	reason = strings.TrimSpace(reason)
	if status == statusNotLaunched {
		if reason == "" {
			return errors.New("informe o motivo pelo qual o lançamento não foi lançado")
		}
		if utf8.RuneCountInString(reason) > 500 {
			return errors.New("o motivo do não lançamento deve ter no máximo 500 caracteres")
		}
		if strings.IndexByte(reason, 0) >= 0 {
			return errors.New("motivo do não lançamento inválido")
		}
	} else {
		reason = ""
	}
	if err := s.EnsureReviewSchema(); err != nil {
		return err
	}

	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	rows, err := s.db.queryUnlocked("SELECT status FROM payments WHERE id=" + sqlI(id) + " LIMIT 1;")
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("lançamento não encontrado")
	}
	if rows[0]["status"] == statusReceitaPosted {
		return errors.New("este lançamento já foi confirmado no Receita Saúde e está bloqueado para edição")
	}
	q := "UPDATE payments SET status=" + sqlQ(status) + ",non_launch_reason=" + sqlQ(reason) + ",receita_confirmed_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=" + sqlI(id) + ";"
	return s.db.execUnlocked(q)
}

func (s *sqliteStore) ConfirmReceita(professionalID int64, year, month int) (int, error) {
	if professionalID <= 0 || year < 2000 || year > 2100 || month < 1 || month > 12 {
		return 0, errors.New("profissional/competência inválidos")
	}
	if err := s.EnsureReviewSchema(); err != nil {
		return 0, err
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	competence := fmt.Sprintf("%04d-%02d", year, month)
	q := "UPDATE payments SET status='LANCADO_RECEITA_SAUDE',receita_confirmed_at=CURRENT_TIMESTAMP,non_launch_reason='',updated_at=CURRENT_TIMESTAMP WHERE professional_id=" + sqlI(professionalID) + " AND substr(payment_date,1,7)=" + sqlQ(competence) + " AND status='VALIDADO_RECEITA_SAUDE';"
	if err := s.db.execUnlocked(q); err != nil {
		return 0, err
	}
	return s.db.changesUnlocked(), nil
}

func (s *sqliteStore) ReviewMeta(id int64) (paymentReviewMeta, error) {
	rows, err := s.db.Query("SELECT non_launch_reason,COALESCE(receita_confirmed_at,'') AS receita_confirmed_at FROM payments WHERE id=" + sqlI(id) + " LIMIT 1;")
	if err != nil {
		return paymentReviewMeta{}, err
	}
	if len(rows) == 0 {
		return paymentReviewMeta{}, errors.New("lançamento não encontrado")
	}
	return paymentReviewMeta{NonLaunchReason: rows[0]["non_launch_reason"], ReceitaConfirmedAt: rows[0]["receita_confirmed_at"]}, nil
}

func reviewStatusLabel(status string) string {
	switch status {
	case statusPending:
		return "Pendente de revisão"
	case statusReceitaReady:
		return "Validado para Receita Saúde"
	case statusInvoiceReady:
		return "Validado para Nota Fiscal"
	case statusNotLaunched:
		return "Não lançado"
	case statusReceitaPosted:
		return "Lançado no Receita Saúde"
	case "EXPORTADO_CSV":
		return "Validado para Receita Saúde"
	default:
		return status
	}
}

func reviewStatusOptions(selected string, filter bool) string {
	items := []string{statusPending, statusReceitaReady, statusInvoiceReady, statusNotLaunched}
	if filter {
		items = append([]string{""}, items...)
		items = append(items, statusReceitaPosted)
	}
	var b strings.Builder
	for _, v := range items {
		label := reviewStatusLabel(v)
		if v == "" {
			label = "Todos"
		}
		sel := ""
		if v == selected {
			sel = " selected"
		}
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, esc(v), sel, esc(label))
	}
	return b.String()
}

func reviewReturnPath(r *http.Request) string {
	q := url.Values{}
	if v := strings.TrimSpace(r.FormValue("filter_professional_id")); v != "" {
		q.Set("professional_id", v)
	}
	if v := strings.TrimSpace(r.FormValue("filter_year")); v != "" {
		q.Set("year", v)
	}
	if v := strings.TrimSpace(r.FormValue("filter_month")); v != "" {
		q.Set("month", v)
	}
	if v := strings.TrimSpace(r.FormValue("filter_status")); v != "" {
		q.Set("status", v)
	}
	if len(q) == 0 {
		return "/payments"
	}
	return "/payments?" + q.Encode()
}

func redirectReviewMsg(w http.ResponseWriter, r *http.Request, msg, kind string) {
	base := reviewReturnPath(r)
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	q := url.Values{"msg": {msg}}
	if kind != "" {
		q.Set("type", kind)
	}
	http.Redirect(w, r, base+sep+q.Encode(), http.StatusSeeOther)
}

func (a *appServer) paymentsV2(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	rs, err := asReviewStore(a.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rs.EnsureReviewSchema(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	f := parseFilter(r)
	payments, err := a.store.ListPayments(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	professionals, _ := a.store.ListProfessionals()
	var rows strings.Builder
	var total int64
	for _, p := range payments {
		total += p.AmountCents
		meta, err := rs.ReviewMeta(p.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		statusCell := `<span class="pill">` + esc(reviewStatusLabel(p.Status)) + `</span>`
		if meta.NonLaunchReason != "" {
			statusCell += `<div class="muted" style="margin-top:6px"><b>Motivo:</b> ` + esc(meta.NonLaunchReason) + `</div>`
		}
		if p.Status == statusReceitaPosted && meta.ReceitaConfirmedAt != "" {
			statusCell += `<div class="muted" style="margin-top:6px">Confirmado em ` + esc(meta.ReceitaConfirmedAt) + `</div>`
		}

		action := `<span class="muted">Bloqueado após confirmação no Receita Saúde.</span>`
		if p.Status != statusReceitaPosted {
			action = `<form method="post" action="/payments/review">` + a.csrfField() +
				`<input type="hidden" name="payment_id" value="` + strconv.FormatInt(p.ID, 10) + `">` +
				`<input type="hidden" name="filter_professional_id" value="` + strconv.FormatInt(f.ProfessionalID, 10) + `">` +
				`<input type="hidden" name="filter_year" value="` + func() string { if f.Year == 0 { return "" }; return strconv.Itoa(f.Year) }() + `">` +
				`<input type="hidden" name="filter_month" value="` + func() string { if f.Month == 0 { return "" }; return strconv.Itoa(f.Month) }() + `">` +
				`<input type="hidden" name="filter_status" value="` + esc(f.Status) + `">` +
				`<label>Decisão</label><select name="status" required>` + reviewStatusOptions(p.Status, false) + `</select>` +
				`<label style="margin-top:7px">Motivo (obrigatório se “Não lançado”)</label><input name="non_launch_reason" maxlength="500" value="` + esc(meta.NonLaunchReason) + `" placeholder="Informe o motivo quando não houver lançamento">` +
				`<button style="margin-top:7px" type="submit">Salvar decisão</button></form>`
		}

		fmt.Fprintf(&rows, `<tr data-payment-id="%d"><td>%s</td><td>%s</td><td>%s<br><span class="muted">%s</span></td><td>%s</td><td>%s</td><td>%s</td><td style="min-width:260px">%s</td></tr>`,
			p.ID, esc(p.PaymentDate), esc(p.Professional), esc(p.PayerName), esc(p.PayerCPF), esc(p.BeneficiaryName), esc(formatMoney(p.AmountCents)), statusCell, action)
	}
	if len(payments) == 0 {
		rows.WriteString(`<tr><td colspan="7" class="muted">Nenhum lançamento para os filtros escolhidos.</td></tr>`)
	}

	yearValue := ""
	if f.Year != 0 {
		yearValue = strconv.Itoa(f.Year)
	}
	validationYear := "2026"
	if f.Year != 0 {
		validationYear = strconv.Itoa(f.Year)
	}
	body := messageFrom(r) + `<h1>Escrituração</h1>` +
		`<div class="card"><h2>Filtros de revisão</h2><form method="get"><div class="row"><div><label>Profissional</label><select name="professional_id">` + professionalSelect(professionals, f.ProfessionalID) + `</select></div><div><label>Ano</label><input name="year" type="number" min="2000" max="2100" value="` + yearValue + `"></div><div><label>Mês</label><select name="month">` + monthOptions(f.Month) + `</select></div><div><label>Status</label><select name="status">` + reviewStatusOptions(f.Status, true) + `</select></div></div><p><button>Filtrar</button></p></form></div>` +
		`<div class="grid"><div class="metric"><b>` + strconv.Itoa(len(payments)) + `</b>lançamentos exibidos</div><div class="metric"><b>` + esc(formatMoney(total)) + `</b>total exibido</div></div>` +
		`<div class="card" style="margin-top:18px"><h2>Revisão individual</h2><p class="muted">Cada item pode ser alterado entre Pendente, Receita Saúde, Nota Fiscal e Não lançado até a confirmação final do lançamento no Receita Saúde.</p><div class="scroll"><table><thead><tr><th>Data</th><th>Profissional</th><th>Pagador</th><th>Beneficiário</th><th>Valor</th><th>Status / motivo</th><th>Revisão</th></tr></thead><tbody>` + rows.String() + `</tbody></table></div></div>` +
		`<div class="card"><h2>Atalho: validar pendentes para Receita Saúde</h2><p class="muted">Este comando altera somente itens ainda PENDENTE_REVISAO; decisões individuais já tomadas são preservadas.</p><form method="post" action="/payments/validate">` + a.csrfField() + `<div class="row"><div><label>Profissional</label><select name="professional_id" required>` + professionalSelect(professionals, f.ProfessionalID) + `</select></div><div><label>Ano</label><input name="year" type="number" min="2000" max="2100" value="` + validationYear + `" required></div><div><label>Mês</label><select name="month" required>` + monthOptions(f.Month) + `</select></div><div class="actions"><button>Validar pendentes</button></div></div></form></div>`
	_, _ = io.WriteString(w, page("Escrituração", body))
}

func (a *appServer) reviewPayment(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	if !a.checkCSRF(r) {
		http.Error(w, "requisição inválida", http.StatusBadRequest)
		return
	}
	id, err := parseID(r.FormValue("payment_id"))
	if err != nil {
		redirectReviewMsg(w, r, err.Error(), "error")
		return
	}
	rs, err := asReviewStore(a.store)
	if err == nil {
		err = rs.ReviewPayment(id, r.FormValue("status"), r.FormValue("non_launch_reason"))
	}
	if err != nil {
		redirectReviewMsg(w, r, err.Error(), "error")
		return
	}
	redirectReviewMsg(w, r, "Decisão do lançamento atualizada.", "")
}

func (a *appServer) confirmReceita(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	if !a.checkCSRF(r) {
		http.Error(w, "requisição inválida", http.StatusBadRequest)
		return
	}
	pid, err := parseID(r.FormValue("professional_id"))
	year, yearErr := strconv.Atoi(r.FormValue("year"))
	month, monthErr := strconv.Atoi(r.FormValue("month"))
	if err != nil || yearErr != nil || monthErr != nil {
		redirectMsg(w, r, "/export", "Profissional/competência inválidos.", "error")
		return
	}
	rs, err := asReviewStore(a.store)
	if err != nil {
		redirectMsg(w, r, "/export", err.Error(), "error")
		return
	}
	count, err := rs.ConfirmReceita(pid, year, month)
	if err != nil {
		redirectMsg(w, r, "/export", err.Error(), "error")
		return
	}
	redirectMsg(w, r, "/export", fmt.Sprintf("%d lançamentos confirmados como lançados no Receita Saúde.", count), "")
}

func (a *appServer) exportPageV2(w http.ResponseWriter, r *http.Request) {
	rs, err := asReviewStore(a.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rs.EnsureReviewSchema(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	professionals, _ := a.store.ListProfessionals()
	if r.Method == http.MethodGet {
		body := messageFrom(r) + `<h1>Exportar escrituração</h1>` +
			`<div class="card"><h2>Gerar CSV para o Carnê-Leão</h2><p class="muted">Entram no CSV exclusivamente os itens “Validado para Receita Saúde”. Gerar ou baixar o CSV não bloqueia a revisão e não muda o status dos lançamentos.</p><form method="post" action="/export">` + a.csrfField() + `<div class="row"><div><label>Profissional</label><select name="professional_id" required>` + professionalSelect(professionals, 0) + `</select></div><div><label>Ano</label><input type="number" name="year" min="2000" max="2100" value="2026" required></div><div><label>Mês</label><select name="month" required>` + monthOptions(0) + `</select></div><div class="actions"><button>Gerar CSV</button></div></div></form></div>` +
			`<div class="card"><h2>Confirmação final</h2><p><b>Use somente depois de efetivamente lançar os registros no Receita Saúde.</b></p><p class="muted">O comando abaixo sela apenas os itens que, naquele momento, estiverem “Validado para Receita Saúde”. Depois disso eles passam a “Lançado no Receita Saúde” e ficam bloqueados para edição.</p><form method="post" action="/payments/confirm-receita">` + a.csrfField() + `<div class="row"><div><label>Profissional</label><select name="professional_id" required>` + professionalSelect(professionals, 0) + `</select></div><div><label>Ano</label><input type="number" name="year" min="2000" max="2100" value="2026" required></div><div><label>Mês</label><select name="month" required>` + monthOptions(0) + `</select></div><div class="actions"><button class="gold">Confirmar lançados no Receita Saúde</button></div></div></form></div>`
		_, _ = io.WriteString(w, page("Exportar", body))
		return
	}
	if r.Method != http.MethodPost || !a.checkCSRF(r) {
		http.Error(w, "requisição inválida", http.StatusBadRequest)
		return
	}
	pid, err := parseID(r.FormValue("professional_id"))
	if err != nil {
		redirectMsg(w, r, "/export", err.Error(), "error")
		return
	}
	year, _ := strconv.Atoi(r.FormValue("year"))
	month, _ := strconv.Atoi(r.FormValue("month"))
	if year < 2000 || year > 2100 || month < 1 || month > 12 {
		redirectMsg(w, r, "/export", "Competência inválida.", "error")
		return
	}
	prof, err := a.store.GetProfessional(pid)
	if err != nil {
		redirectMsg(w, r, "/export", err.Error(), "error")
		return
	}
	payments, err := a.store.ListPayments(PaymentFilter{ProfessionalID: pid, Year: year, Month: month, Status: statusReceitaReady})
	if err != nil {
		redirectMsg(w, r, "/export", err.Error(), "error")
		return
	}
	if len(payments) == 0 {
		redirectMsg(w, r, "/export", "Não há lançamentos validados para Receita Saúde neste profissional e competência.", "error")
		return
	}
	if len(payments) > 1000 {
		redirectMsg(w, r, "/export", "A competência excede 1.000 linhas; divida a escrituração antes de exportar.", "error")
		return
	}
	data, err := receitaCSV(prof, payments)
	if err != nil {
		redirectMsg(w, r, "/export", err.Error(), "error")
		return
	}
	name := fmt.Sprintf("receita_saude_%04d_%02d_%s.csv", year, month, prof.CPF)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
