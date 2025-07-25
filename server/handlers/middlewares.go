// Package handlers :  collection of handlers (aka "HTTP middleware")
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gofrs/uuid"
	"github.com/meshery/meshery/server/machines"
	mhelpers "github.com/meshery/meshery/server/machines/helpers"
	"github.com/meshery/meshery/server/machines/kubernetes"
	"github.com/meshery/meshery/server/models"
	"github.com/meshery/meshkit/utils"
	"github.com/meshery/meshsync/pkg/model"
	"github.com/spf13/viper"
)

const providerQParamName = "provider"

// ProviderMiddleware is a middleware to validate if a provider is set
func (h *Handler) ProviderMiddleware(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, req *http.Request) {
		var providerName string
		var provider models.Provider
		ck, err := req.Cookie(h.config.ProviderCookieName)
		if err == nil {
			providerName = ck.Value
		} else {
			providerName = req.Header.Get(h.config.ProviderCookieName)
			// allow provider to be set using query parameter
			// this is OK since provider information is not sensitive

			if providerName == "" {
				providerName = req.URL.Query().Get(providerQParamName)
			}
		}
		if providerName != "" {
			provider = h.config.Providers[providerName]
		}
		ctx := context.WithValue(req.Context(), models.ProviderCtxKey, provider) // nolint

		// Incase Meshery is configured for deployments scenario: Istio, Azure Kubernetes Service etc
		// then we can expect a MESHERY_SERVER_CALLBACK_URL in env var
		callbackURL := viper.GetString("MESHERY_SERVER_CALLBACK_URL")
		if callbackURL == "" {
			// if MESHERY_SERVER_CALLBACK_URL is not set then we can assume standard CALLBACK_URL
			callbackURL = "http://" + req.Host + "/api/user/token" // Hard coding the path because this is what meshery expects
		}
		ctx = context.WithValue(ctx, models.MesheryServerCallbackURL, callbackURL)
		_url, err := url.Parse(callbackURL)
		if err != nil {
			h.log.Error(ErrParsingCallBackUrl(err))
		} else {
			ctx = context.WithValue(ctx, models.MesheryServerURL, fmt.Sprintf("%s://%s", _url.Scheme, _url.Host))
		}
		req1 := req.WithContext(ctx)
		next.ServeHTTP(w, req1)
	}
	return http.HandlerFunc(fn)
}
func (h *Handler) NoCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Set headers to disable caching
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		// Call the next handler
		next.ServeHTTP(w, req)
	})
}

// AuthMiddleware is a middleware to validate if a user is authenticated
func (h *Handler) AuthMiddleware(next http.Handler, auth models.AuthenticationMechanism) http.Handler {
	fn := func(w http.ResponseWriter, req *http.Request) {
		refURLB64 := GetRefURL(req)

		providerH := h.Provider
		if auth == models.NoAuth && providerH != "" {
			auth = models.ProviderAuth //If a provider is enforced then use provider authentication even in case of NoAuth
		}
		switch auth {
		// case models.NoAuth:
		// 	if providerH != "" {
		// 		w.WriteHeader(http.StatusUnauthorized)
		// 		return
		// 	}
		case models.ProviderAuth:
			// Propagate existing request parameters, if present.
			queryParams := req.URL.Query()
			if refURLB64 != "" {
				queryParams["ref"] = []string{refURLB64}
			}
			providerI := req.Context().Value(models.ProviderCtxKey)
			// logrus.Debugf("models.ProviderCtxKey %s", models.ProviderCtxKey)
			provider, ok := providerI.(models.Provider)
			if !ok {
				http.Redirect(w, req, fmt.Sprintf("/provider?%s", queryParams.Encode()), http.StatusFound)
				return
			}

			// Because server verifies the value of the "PROVIDER" environemnt variable and doesn't allow unsupported provider value,
			// the below situation cannot occur.

			// if providerH != "" && providerH != provider.Name() {
			// 	w.WriteHeader(http.StatusUnauthorized)
			// 	return
			// }
			// logrus.Debugf("provider %s", provider)
			isValid, err := h.validateAuth(provider, req)
			// logrus.Debugf("validate auth: %t", isValid)
			if !isValid {
				if !errors.Is(err, models.ErrEmptySession) && provider.GetProviderType() == models.RemoteProviderType {
					provider.HandleUnAuthenticated(w, req)
					return
				}

				// Local Provider
				h.LoginHandler(w, req, provider, true)
				return
			}
		}
		next.ServeHTTP(w, req)
	}
	return http.HandlerFunc(fn)
}

func (h *Handler) validateAuth(provider models.Provider, req *http.Request) (bool, error) {
	err := provider.GetSession(req)
	if err == nil {
		// logrus.Debugf("session: %v", sess)
		return true, nil
	}
	// logrus.Errorf("session invalid, error: %v", err)
	return false, err
}

// KubernetesMiddleware is a middleware that is responsible for handling kubernetes related stuff such as
// setting contexts, component generation etc.
func (h *Handler) KubernetesMiddleware(next func(http.ResponseWriter, *http.Request, *models.Preference, *models.User, models.Provider)) func(http.ResponseWriter, *http.Request, *models.Preference, *models.User, models.Provider) {
	return func(w http.ResponseWriter, req *http.Request, prefObj *models.Preference, user *models.User, provider models.Provider) {
		ctx := req.Context()
		ctx, err := KubernetesMiddleware(ctx, h, provider, user, req.URL.Query()["contexts"])
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		req1 := req.WithContext(ctx)
		next(w, req1, prefObj, user, provider)
	}
}

// SessionInjectorMiddleware - is a middleware which injects user and session object
func (h *Handler) SessionInjectorMiddleware(next func(http.ResponseWriter, *http.Request, *models.Preference, *models.User, models.Provider)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerI := req.Context().Value(models.ProviderCtxKey)
		provider, ok := providerI.(models.Provider)
		if !ok {
			http.Redirect(w, req, "/provider", http.StatusFound)
			return
		}
		// ensuring session is intact
		// err := provider.GetSession(req)
		// if err != nil {
		// 	err1 := provider.Logout(w, req)
		// 	if err1 != nil {
		// 		logrus.Errorf("Error performing logout: %v", err1.Error())
		// 		provider.HandleUnAuthenticated(w, req)
		// 		return
		// 	}
		// 	logrus.Errorf("Error: unable to get session: %v", err)
		// 	http.Error(w, "unable to get session", http.StatusUnauthorized)
		// 	return
		// }

		user, err := provider.GetUserDetails(req)
		// if user details are not available,
		// then logout current user session and redirect to login page
		if err != nil {
			err1 := provider.Logout(w, req)
			if err1 != nil {
				h.log.Error(models.ErrLogout(err1))
				provider.HandleUnAuthenticated(w, req)
				return
			}
			h.log.Error(ErrGetUserDetails(err))
			http.Error(w, ErrGetUserDetails(err).Error(), http.StatusUnauthorized)
			return
		}
		prefObj, err := provider.ReadFromPersister(user.UserID)
		if err != nil {
			h.log.Warn(ErrReadSessionPersistor)
		}

		token := provider.UpdateToken(w, req)
		ctx := context.WithValue(req.Context(), models.TokenCtxKey, token)
		ctx = context.WithValue(ctx, models.PerfObjCtxKey, prefObj)
		ctx = context.WithValue(ctx, models.UserCtxKey, user)
		ctx = context.WithValue(ctx, models.RegistryManagerKey, h.registryManager)
		ctx = context.WithValue(ctx, models.HandlerKey, h)
		ctx = context.WithValue(ctx, models.SystemIDKey, h.SystemID)
		req1 := req.WithContext(ctx)

		next(w, req1, prefObj, user, provider)
	})
}

// GraphqlSessionInjectorMiddleware - is a middleware which injects user and session object
func (h *Handler) GraphqlMiddleware(next http.Handler) func(http.ResponseWriter, *http.Request, *models.Preference, *models.User, models.Provider) {
	return func(w http.ResponseWriter, req *http.Request, pref *models.Preference, user *models.User, prov models.Provider) {
		next.ServeHTTP(w, req)
	}
}

func KubernetesMiddleware(ctx context.Context, h *Handler, provider models.Provider, user *models.User, k8sContextIDs []string) (context.Context, error) {
	token, ok := ctx.Value(models.TokenCtxKey).(string)
	if !ok {
		err := ErrRetrieveUserToken(fmt.Errorf("failed to retrieve user token"))
		h.log.Error(err)
		return nil, err
	}
	userUUID := uuid.FromStringOrNil(user.ID)
	smInstanceTracker := h.ConnectionToStateMachineInstanceTracker
	connectedK8sContexts, err := provider.LoadAllK8sContext(token)

	k8sContextPassedByUser := []models.K8sContext{}
	k8sContextsFromKubeConfig := []*models.K8sContext{}

	if err != nil || len(connectedK8sContexts) == 0 {
		h.log.Warn(ErrFailToGetK8SContext)
		k8sContextsFromKubeConfig, err = h.DiscoverK8SContextFromKubeConfig(user.ID, token, provider)
		if err != nil {
			h.log.Warn(ErrFailToLoadK8sContext(err))
		}
	}

	if len(k8sContextIDs) == 1 && k8sContextIDs[0] == "all" {
		for _, c := range connectedK8sContexts {
			if c != nil {
				k8sContextPassedByUser = append(k8sContextPassedByUser, *c)
			}
		}
	} else {
		for _, kctxID := range k8sContextIDs {
			for _, c := range connectedK8sContexts {
				if c != nil && c.ID == kctxID {
					k8sContextPassedByUser = append(k8sContextPassedByUser, *c)
				}
			}
		}
	}

	ctx = context.WithValue(ctx, models.KubeClustersKey, k8sContextPassedByUser)
	ctx = context.WithValue(ctx, models.AllKubeClusterKey, connectedK8sContexts)

	for _, k8sContext := range k8sContextsFromKubeConfig {
		machineCtx := &kubernetes.MachineCtx{
			K8sContext:         *k8sContext,
			MesheryCtrlsHelper: h.MesheryCtrlsHelper,
			K8sCompRegHelper:   h.K8sCompRegHelper,
			OperatorTracker:    h.config.OperatorTracker,
			K8scontextChannel:  h.config.K8scontextChannel,
			EventBroadcaster:   h.config.EventBroadcaster,
			RegistryManager:    h.registryManager,
		}
		connectionUUID := uuid.FromStringOrNil(k8sContext.ConnectionID)

		inst, err := mhelpers.InitializeMachineWithContext(
			machineCtx,
			ctx,
			connectionUUID,
			userUUID,
			smInstanceTracker,
			h.log,
			provider,
			machines.DefaultState,
			"kubernetes",
			kubernetes.AssignInitialCtx,
		)
		if err != nil {
			h.log.Error(err)
		}

		inst.ResetState()
		go func(inst *machines.StateMachine) {
			event, err := inst.SendEvent(ctx, machines.Discovery, nil)
			if err != nil {
				_ = provider.PersistEvent(*event, nil)
				go h.config.EventBroadcaster.Publish(userUUID, event)
			}
		}(inst)
	}
	return ctx, nil
}

func (h *Handler) K8sFSMMiddleware(next func(http.ResponseWriter, *http.Request, *models.Preference, *models.User, models.Provider)) func(http.ResponseWriter, *http.Request, *models.Preference, *models.User, models.Provider) {
	return func(w http.ResponseWriter, req *http.Request, prefObj *models.Preference, user *models.User, provider models.Provider) {
		K8sFSMMiddleware(req.Context(), h, provider, user)
		next(w, req, prefObj, user, provider)
	}
}

type dataHandlerToClusterID struct {
	mdh       models.MeshsyncDataHandler
	clusterID string
}

func K8sFSMMiddleware(ctx context.Context, h *Handler, provider models.Provider, user *models.User) {
	smInstanceTracker := h.ConnectionToStateMachineInstanceTracker
	connectedK8sContexts := ctx.Value(models.AllKubeClusterKey).([]*models.K8sContext)
	userUUID := uuid.FromStringOrNil(user.ID)
	dataHandlers := []*dataHandlerToClusterID{}
	clusterIDs := []string{}
	for _, k8sContext := range connectedK8sContexts {
		machineCtx := &kubernetes.MachineCtx{
			K8sContext:         *k8sContext,
			MesheryCtrlsHelper: h.MesheryCtrlsHelper,
			K8sCompRegHelper:   h.K8sCompRegHelper,
			OperatorTracker:    h.config.OperatorTracker,
			K8scontextChannel:  h.config.K8scontextChannel,
			EventBroadcaster:   h.config.EventBroadcaster,
			RegistryManager:    h.registryManager,
		}
		connectionUUID := uuid.FromStringOrNil(k8sContext.ConnectionID)

		inst, err := mhelpers.InitializeMachineWithContext(
			machineCtx,
			ctx,
			connectionUUID,
			userUUID,
			smInstanceTracker,
			h.log,
			provider,
			machines.DefaultState,
			"kubernetes",
			kubernetes.AssignInitialCtx,
		)
		if err != nil {
			h.log.Error(err)
		}

		inst.ResetState()
		go func(inst *machines.StateMachine) {
			event, err := inst.SendEvent(ctx, machines.Discovery, nil)
			if err != nil {
				_ = provider.PersistEvent(*event, nil)
				go h.config.EventBroadcaster.Publish(userUUID, event)
			}
		}(inst)
		kubernesMachineCtx, err := utils.Cast[*kubernetes.MachineCtx](inst.Context)
		if err != nil {
			h.log.Error(err)
			continue
		}
		mdh := kubernesMachineCtx.MesheryCtrlsHelper.GetMeshSyncDataHandlersForEachContext()
		if mdh != nil {
			dataHandlers = append(dataHandlers, &dataHandlerToClusterID{
				mdh:       *mdh,
				clusterID: k8sContext.KubernetesServerID.String(),
			})
			clusterIDs = append(clusterIDs, k8sContext.KubernetesServerID.String())
		}
	}
	var resources []model.KubernetesResource

	err := provider.GetGenericPersister().Model(&model.KubernetesResource{}).
		Preload("KubernetesResourceMeta").
		Joins("JOIN kubernetes_resource_object_meta ON kubernetes_resource_object_meta.id = kubernetes_resources.id").
		Where("kubernetes_resources.cluster_id IN (?)", clusterIDs).Where(&model.KubernetesResource{Kind: "Service"}).Where("lower(kubernetes_resource_object_meta.name) LIKE ? OR lower(kubernetes_resource_object_meta.name) LIKE ?", "%grafana%", "%prometheus%").Find(&resources).Error

	if err != nil {
		h.log.Error(ErrFetchMeshSyncResources(err))
		return
	}

	regQueue := models.GetMeshSyncRegistrationQueue()

	for _, resource := range resources {
		for _, dh := range dataHandlers {
			if dh.clusterID == resource.ClusterID {
				go regQueue.Send(models.MeshSyncRegistrationData{MeshsyncDataHandler: dh.mdh, Obj: resource})
			}
		}
	}

}
