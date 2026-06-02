package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type fakeSLAService struct {
	appID  string
	window model.Window
}

func (f *fakeSLAService) GetAppSLA(_ context.Context, appID string, window model.Window) (model.SLAStatus, error) {
	f.appID = appID
	f.window = window
	return model.SLAStatus{AppID: appID, Window: window, Current: 0.9995, Target: 0.999, MeetingTarget: true}, nil
}

func TestServerHandlesAppSLA(t *testing.T) {
	sla := &fakeSLAService{}
	s := New(Config{SLAService: sla})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/apps/app-a/sla?window=10m", nil)
	rec := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if sla.appID != "app-a" {
		t.Fatalf("appID = %q; want app-a", sla.appID)
	}
	if sla.window != model.Window10m {
		t.Fatalf("window = %q; want 10m", sla.window)
	}
	if !strings.Contains(rec.Body.String(), `"current":0.9995`) {
		t.Fatalf("response body = %s", rec.Body.String())
	}
}
