package controller

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const karmadaDashboardSessionCookie = "newapi_karmada_session"

func CreateKarmadaDashboardSession(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "code": "AUTH_SESSION_REQUIRED"})
		return
	}

	sessionToken, expiresAt, err := service.IssueKarmadaDashboardSession(identity)
	if err != nil {
		common.SysError("issue Karmada dashboard session: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "code": "KARMADA_SESSION_ISSUE_FAILED"})
		return
	}

	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(karmadaDashboardSessionCookie, sessionToken, int(service.KarmadaDashboardSessionTTL.Seconds()), "/karmada-dashboard", "", common.SessionCookieSecure, true)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"expires_at": expiresAt}})
}

func ProxyKarmadaDashboard(c *gin.Context) {
	sessionCookie, err := c.Cookie(karmadaDashboardSessionCookie)
	if err != nil {
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusUnauthorized)
		return
	}
	identity, err := service.ValidateKarmadaDashboardSession(sessionCookie)
	if err != nil {
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusUnauthorized)
		return
	}

	_, user, err := service.ValidateLoginSession(identity)
	if err != nil || user.Role != common.RoleRootUser || user.Status != common.UserStatusEnabled {
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusForbidden)
		return
	}

	upstreamRaw := strings.TrimSpace(os.Getenv("KARMADA_DASHBOARD_URL"))
	credential := strings.TrimSpace(os.Getenv("KARMADA_DASHBOARD_TOKEN"))
	if upstreamRaw == "" || credential == "" {
		common.SysError("Karmada dashboard proxy is not configured")
		c.Status(http.StatusServiceUnavailable)
		return
	}
	upstream, err := url.Parse(upstreamRaw)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		common.SysError("invalid Karmada dashboard upstream URL")
		c.Status(http.StatusServiceUnavailable)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.URL.Path = strings.TrimPrefix(c.Request.URL.Path, "/karmada-dashboard")
		if request.URL.Path == "" {
			request.URL.Path = "/"
		}
		request.URL.Path = strings.TrimSuffix(upstream.Path, "/") + request.URL.Path
		request.URL.RawQuery = c.Request.URL.RawQuery
		request.Host = upstream.Host
		request.Header = c.Request.Header.Clone()
		request.Header.Del("Cookie")
		request.Header.Del("Authorization")
		request.Header.Del("Impersonate-User")
		request.Header.Del("Impersonate-Group")
		for key := range request.Header {
			if strings.HasPrefix(strings.ToLower(key), "impersonate-extra-") {
				request.Header.Del(key)
			}
		}
		if strings.HasPrefix(c.Request.URL.Path, "/karmada-dashboard/api/") || strings.HasPrefix(c.Request.URL.Path, "/karmada-dashboard/clusterapi/") {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Del("Set-Cookie")
		response.Header.Set("Content-Security-Policy", "frame-ancestors 'self'")
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, proxyErr error) {
		common.SysError("Karmada dashboard proxy error: " + proxyErr.Error())
		writer.WriteHeader(http.StatusBadGateway)
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}
