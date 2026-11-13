package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/auth"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func postCLITest(t *testing.T, h *usersHandlers, user *auth.User, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/users/"+id+"/cli-test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", id)
	if user != nil {
		req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, user))
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.testCLI(c)
	return rec
}

func TestCLITestRejectsAnonymousAndUnknownCLI(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	user, err := store.CreateLocalAccount("cli-test", "secret1", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &usersHandlers{deps: &deps{repo: store}}

	// Only the signed-in owner may run the test.
	id := strconv.FormatInt(user.ID, 10)
	if rec := postCLITest(t, h, nil, id, `{"engine":"kimi"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous: %d %s", rec.Code, rec.Body.String())
	}
	other := *domainUserToAuth(user)
	other.ID++
	if rec := postCLITest(t, h, &other, id, `{"engine":"kimi"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("other user: %d %s", rec.Code, rec.Body.String())
	}
	// The body selects an allowlisted engine, never a command or package.
	if rec := postCLITest(t, h, domainUserToAuth(user), id, `{"engine":"rm -rf /"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown engine: %d %s", rec.Code, rec.Body.String())
	}
}
