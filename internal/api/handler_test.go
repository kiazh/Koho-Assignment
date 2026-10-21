package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"koho-assignment/internal/ledger"
)

func testAPI(t *testing.T) http.Handler {
	t.Helper()
	store, err := ledger.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store)
}

func request(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
	return recorder
}

func createAccount(t *testing.T, handler http.Handler, cents int64) ledger.Account {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/accounts", `{"initial_balance_cents":`+strconv.FormatInt(cents, 10)+`}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("open account status = %d: %s", response.Code, response.Body.String())
	}
	var account ledger.Account
	if err := json.Unmarshal(response.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	return account
}

func TestAccountAndTransferRoutes(t *testing.T) {
	handler := testAPI(t)
	from := createAccount(t, handler, 100)
	to := createAccount(t, handler, 0)
	transferBody := `{"from_account_id":"` + from.ID + `","to_account_id":"` + to.ID + `","amount_cents":40,"idempotency_key":"route-once"}`
	created := request(t, handler, http.MethodPost, "/transfers", transferBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("transfer status = %d: %s", created.Code, created.Body.String())
	}
	var first ledger.Transfer
	if err := json.Unmarshal(created.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	replayed := request(t, handler, http.MethodPost, "/transfers", transferBody)
	var second ledger.Transfer
	_ = json.Unmarshal(replayed.Body.Bytes(), &second)
	if replayed.Code != http.StatusOK || first.ID == "" || second.ID != first.ID {
		t.Fatalf("replay status=%d first=%+v second=%+v", replayed.Code, first, second)
	}
	balance := request(t, handler, http.MethodGet, "/accounts/"+from.ID+"/balance", "")
	if balance.Code != http.StatusOK || !bytes.Contains(balance.Body.Bytes(), []byte(`"balance_cents":60`)) {
		t.Fatalf("balance status=%d body=%s", balance.Code, balance.Body.String())
	}
	history := request(t, handler, http.MethodGet, "/accounts/"+to.ID+"/transactions", "")
	if history.Code != http.StatusOK || !bytes.Contains(history.Body.Bytes(), []byte(first.ID)) {
		t.Fatalf("history status=%d body=%s", history.Code, history.Body.String())
	}
}

func TestStrictMoneyJSONAndErrorStatuses(t *testing.T) {
	handler := testAPI(t)
	for _, body := range []string{
		`{"initial_balance_cents":10.5}`,
		`{"initial_balance_cents":1e2}`,
		`{"initial_balance_cents":"100"}`,
		`{"initial_balance_cents":-1}`,
		`{"initial_balance_cents":9223372036854775808}`,
		`{"initial_balance_cents":0,"extra":1}`,
		`{"initial_balance_cents":0}{}`,
	} {
		response := request(t, handler, http.MethodPost, "/accounts", body)
		if response.Code != http.StatusBadRequest {
			t.Errorf("body=%s status=%d want 400", body, response.Code)
		}
	}
	from := createAccount(t, handler, 1)
	to := createAccount(t, handler, 0)
	insufficient := request(t, handler, http.MethodPost, "/transfers", `{"from_account_id":"`+from.ID+`","to_account_id":"`+to.ID+`","amount_cents":2,"idempotency_key":"insufficient"}`)
	if insufficient.Code != http.StatusUnprocessableEntity {
		t.Fatalf("insufficient status=%d body=%s", insufficient.Code, insufficient.Body.String())
	}
	missing := request(t, handler, http.MethodGet, "/accounts/missing/balance", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d", missing.Code)
	}
	created := request(t, handler, http.MethodPost, "/transfers", `{"from_account_id":"`+from.ID+`","to_account_id":"`+to.ID+`","amount_cents":1,"idempotency_key":"conflict"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("created status=%d: %s", created.Code, created.Body.String())
	}
	conflict := request(t, handler, http.MethodPost, "/transfers", `{"from_account_id":"`+from.ID+`","to_account_id":"`+to.ID+`","amount_cents":2,"idempotency_key":"conflict"}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestHTTPBoundaryRejectionsAndReceiverOverflow(t *testing.T) {
	handler := testAPI(t)
	from := createAccount(t, handler, 1)
	to := createAccount(t, handler, math.MaxInt64)

	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{"missing opening balance", "/accounts", `{}`},
		{"zero transfer amount", "/transfers", `{"from_account_id":"` + from.ID + `","to_account_id":"` + to.ID + `","amount_cents":0,"idempotency_key":"zero"}`},
		{"self transfer", "/transfers", `{"from_account_id":"` + from.ID + `","to_account_id":"` + from.ID + `","amount_cents":1,"idempotency_key":"self"}`},
		{"key too long", "/transfers", `{"from_account_id":"` + from.ID + `","to_account_id":"` + to.ID + `","amount_cents":1,"idempotency_key":"` + strings.Repeat("k", 256) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(t, handler, http.MethodPost, tc.path, tc.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s; want 400", response.Code, response.Body.String())
			}
		})
	}

	overflow := request(t, handler, http.MethodPost, "/transfers", `{"from_account_id":"`+from.ID+`","to_account_id":"`+to.ID+`","amount_cents":1,"idempotency_key":"overflow"}`)
	if overflow.Code != http.StatusUnprocessableEntity || !strings.Contains(overflow.Body.String(), `"balance_overflow"`) {
		t.Fatalf("overflow status=%d body=%s", overflow.Code, overflow.Body.String())
	}
	fromBalance := request(t, handler, http.MethodGet, "/accounts/"+from.ID+"/balance", "")
	if !bytes.Contains(fromBalance.Body.Bytes(), []byte(`"balance_cents":1`)) {
		t.Fatalf("source balance changed after overflow: %s", fromBalance.Body.String())
	}
	toBalance := request(t, handler, http.MethodGet, "/accounts/"+to.ID+"/balance", "")
	if !bytes.Contains(toBalance.Body.Bytes(), []byte(`"balance_cents":`+strconv.FormatInt(math.MaxInt64, 10))) {
		t.Fatalf("destination balance changed after overflow: %s", toBalance.Body.String())
	}
}

func TestConcurrentHTTPTransfersCannotDoubleSpend(t *testing.T) {
	handler := testAPI(t)
	from := createAccount(t, handler, 100)
	to := createAccount(t, handler, 0)
	server := httptest.NewServer(handler)
	defer server.Close()
	start := make(chan struct{})
	statuses := make(chan int, 20)
	var ready sync.WaitGroup
	ready.Add(20)
	client := &http.Client{}
	for i := range 20 {
		go func(i int) {
			ready.Done()
			<-start
			body := fmt.Sprintf(`{"from_account_id":%q,"to_account_id":%q,"amount_cents":100,"idempotency_key":%q}`, from.ID, to.ID, fmt.Sprintf("http-race-%d", i))
			response, err := client.Post(server.URL+"/transfers", "application/json", bytes.NewBufferString(body))
			if err != nil {
				statuses <- 0
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			statuses <- response.StatusCode
		}(i)
	}
	ready.Wait()
	close(start)
	created, rejected := 0, 0
	for range 20 {
		switch status := <-statuses; status {
		case http.StatusCreated:
			created++
		case http.StatusUnprocessableEntity:
			rejected++
		default:
			t.Fatalf("unexpected HTTP status %d", status)
		}
	}
	fromResponse := request(t, handler, http.MethodGet, "/accounts/"+from.ID+"/balance", "")
	toResponse := request(t, handler, http.MethodGet, "/accounts/"+to.ID+"/balance", "")
	if created != 1 || rejected != 19 || !bytes.Contains(fromResponse.Body.Bytes(), []byte(`"balance_cents":0`)) || !bytes.Contains(toResponse.Body.Bytes(), []byte(`"balance_cents":100`)) {
		t.Fatalf("created=%d rejected=%d from=%s to=%s", created, rejected, fromResponse.Body.String(), toResponse.Body.String())
	}
}

func TestRouterBoundaryResponses(t *testing.T) {
	handler := testAPI(t)
	unknown := request(t, handler, http.MethodGet, "/unknown", "")
	if unknown.Code != http.StatusNotFound || !strings.HasPrefix(unknown.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("unknown route status=%d content-type=%q", unknown.Code, unknown.Header().Get("Content-Type"))
	}
	method := request(t, handler, http.MethodGet, "/transfers", "")
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != http.MethodPost || !strings.HasPrefix(method.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("wrong method status=%d allow=%q content-type=%q", method.Code, method.Header().Get("Allow"), method.Header().Get("Content-Type"))
	}
}
