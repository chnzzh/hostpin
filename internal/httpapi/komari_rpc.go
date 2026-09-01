package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var errRPCMethodNotFound = errors.New("Method not found")

func (a *API) handleRPC2(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		a.handleRPC2WebSocket(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "RPC2 requires POST or WebSocket")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "Parse error"}})
		return
	}
	if len(raw) > 0 && raw[0] == '[' {
		var requests []rpcRequest
		if err := json.Unmarshal(raw, &requests); err != nil || len(requests) == 0 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "Invalid Request"}})
			return
		}
		responses := make([]rpcResponse, 0, len(requests))
		for _, request := range requests {
			responses = append(responses, a.dispatchRPC(r, request))
		}
		writeJSON(w, http.StatusOK, responses)
		return
	}
	var request rpcRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "Invalid Request"}})
		return
	}
	writeJSON(w, http.StatusOK, a.dispatchRPC(r, request))
}

func (a *API) handleRPC2WebSocket(w http.ResponseWriter, r *http.Request) {
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: a.websocketOriginPatterns()})
	if err != nil {
		return
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	connection.SetReadLimit(2 << 20)
	ctx, cancel := a.connectionContext(r.Context())
	defer cancel()
	r = r.WithContext(ctx)
	for {
		_, payload, err := connection.Read(ctx)
		if err != nil {
			return
		}
		var request rpcRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			continue
		}
		response, _ := json.Marshal(a.dispatchRPC(r, request))
		writeCtx, writeCancel := context.WithTimeout(ctx, 10*time.Second)
		err = connection.Write(writeCtx, websocket.MessageText, response)
		writeCancel()
		if err != nil {
			return
		}
	}
}

func (a *API) dispatchRPC(r *http.Request, request rpcRequest) rpcResponse {
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	if request.JSONRPC != "2.0" || request.Method == "" {
		response.Error = &rpcError{Code: -32600, Message: "Invalid Request"}
		return response
	}
	authenticated, accessErr := a.komariAccess(r)
	settings, _ := a.store.SiteSettings(r.Context())
	loginMethods := map[string]bool{"rpc.ping": true, "rpc.version": true, "public:getMe": true, "public:getPublicSettings": true, "public:getVersion": true, "common:getPublicInfo": true, "common:getVersion": true, "common:getMe": true}
	if accessErr != nil && settings.Private && !loginMethods[request.Method] {
		response.Error = &rpcError{Code: 403, Message: "Private site enabled, please login first"}
		return response
	}
	result, err := a.callRPC(r.Context(), request.Method, request.Params, authenticated)
	if err != nil {
		code := -32602
		if errors.Is(err, errRPCMethodNotFound) {
			code = -32601
		}
		response.Error = &rpcError{Code: code, Message: err.Error()}
		return response
	}
	response.Result = result
	return response
}

func (a *API) callRPC(ctx context.Context, method string, raw json.RawMessage, authenticated bool) (any, error) {
	params := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if raw[0] == '{' {
			_ = json.Unmarshal(raw, &params)
		} else if raw[0] == '[' {
			var positional []any
			_ = json.Unmarshal(raw, &positional)
			params = positionalParams(method, positional)
		}
	}
	switch method {
	case "rpc.ping":
		return "pong", nil
	case "rpc.version":
		return "2.0", nil
	case "rpc.methods":
		return supportedRPCMethods(), nil
	case "common:getPublicInfo", "public:getPublicSettings":
		settings, err := a.store.SiteSettings(ctx)
		if err != nil {
			return nil, err
		}
		return a.komariPublicSettings(ctx, settings), nil
	case "common:getVersion", "public:getVersion":
		return map[string]string{"version": version, "hash": commit}, nil
	case "common:getMe", "public:getMe":
		if !authenticated {
			return map[string]any{"username": "Guest", "logged_in": false}, nil
		}
		return map[string]any{"username": "admin", "logged_in": true}, nil
	case "common:getNodes", "public:getNodesInformation":
		nodes, err := a.komariNodes(ctx, authenticated)
		if err != nil {
			return nil, err
		}
		if method == "public:getNodesInformation" {
			for _, node := range nodes {
				delete(node, "ipv4")
				delete(node, "ipv6")
				delete(node, "version")
				delete(node, "remark")
			}
		}
		if nodeID := stringParam(params, "uuid"); nodeID != "" {
			for _, node := range nodes {
				if node["uuid"] == nodeID {
					return node, nil
				}
			}
			return nil, errors.New("node not found")
		}
		if method == "public:getNodesInformation" {
			return nodes, nil
		}
		mapped := make(map[string]any, len(nodes))
		for _, node := range nodes {
			mapped[fmt.Sprint(node["uuid"])] = node
		}
		return mapped, nil
	case "common:getNodesLatestStatus", "public:getNodesLatestStatus":
		statuses, err := a.komariLatest(ctx, authenticated)
		if err != nil {
			return nil, err
		}
		if nodeID := firstNonEmpty(stringParam(params, "uuid"), stringParam(params, "entity_id")); nodeID != "" {
			status, ok := statuses[nodeID]
			if !ok {
				return nil, errors.New("node not found")
			}
			return status, nil
		}
		if requested := stringSliceParam(params, "uuids"); len(requested) > 0 {
			filtered := make(map[string]map[string]any, len(requested))
			for _, nodeID := range requested {
				if status, ok := statuses[nodeID]; ok {
					filtered[nodeID] = status
				}
			}
			return filtered, nil
		}
		return statuses, nil
	case "common:getNodeRecentStatus", "public:getClientRecentRecords":
		nodeID := stringParam(params, "uuid")
		if err := a.ensureVisibleNode(ctx, nodeID, authenticated); err != nil {
			return nil, err
		}
		records, err := a.store.RecentMetrics(ctx, nodeID, time.Now().Add(-time.Minute))
		if err != nil {
			return nil, err
		}
		converted := make([]map[string]any, 0, len(records))
		for _, record := range records {
			converted = append(converted, komariStatus(record, true))
		}
		if method == "public:getClientRecentRecords" {
			return converted, nil
		}
		return map[string]any{"count": len(converted), "records": converted}, nil
	case "common:getRecords":
		if stringParam(params, "type") == "ping" {
			return a.rpcProbeRecords(ctx, params, authenticated)
		}
		return a.komariLoadRecords(ctx, authenticated, stringParam(params, "uuid"), numberParam(params, "hours", 1), int(numberParam(params, "maxCount", 4000)))
	case "public:getRecordsByUUID":
		return a.komariLoadRecords(ctx, authenticated, stringParam(params, "uuid"), numberParam(params, "hours", 4), 4000)
	case "public:getPingRecords":
		return a.rpcProbeRecords(ctx, params, authenticated)
	case "public:getPublicPingTasks":
		tasks, err := a.store.ListProbeTasks(ctx, "")
		if err == nil {
			tasks, err = a.filterVisibleProbeTasks(ctx, tasks, authenticated)
		}
		return komariTaskList(tasks), err
	case "public:listMetricDefinitions":
		return metricDefinitions(), nil
	case "public:queryMetrics":
		return a.rpcQueryMetrics(ctx, authenticated, params)
	case "public:getPingMetricStats":
		return a.rpcPingStats(ctx, params, authenticated)
	default:
		return nil, errRPCMethodNotFound
	}
}

func positionalParams(method string, values []any) map[string]any {
	keys := map[string][]string{
		"common:getNodes": {"uuid"}, "common:getNodeRecentStatus": {"uuid"},
		"public:getClientRecentRecords": {"uuid"},
		"public:getRecordsByUUID":       {"uuid", "load_type", "hours"},
		"public:getPingRecords":         {"uuid", "task_id", "hours"},
	}
	result := map[string]any{}
	for index, value := range values {
		if index < len(keys[method]) {
			result[keys[method][index]] = value
		}
	}
	return result
}

func supportedRPCMethods() []string {
	return []string{
		"rpc.ping", "rpc.version", "rpc.methods", "common:getNodes",
		"common:getPublicInfo", "common:getVersion", "common:getMe",
		"common:getNodesLatestStatus", "common:getNodeRecentStatus", "common:getRecords",
		"public:getMe", "public:getNodesInformation", "public:getNodesLatestStatus",
		"public:getPublicSettings", "public:getVersion", "public:getClientRecentRecords",
		"public:getRecordsByUUID", "public:getPingRecords", "public:getPublicPingTasks",
		"public:listMetricDefinitions", "public:queryMetrics", "public:getPingMetricStats",
	}
}

func (a *API) rpcProbeRecords(ctx context.Context, params map[string]any, authenticated bool) (map[string]any, error) {
	end := time.Now().UTC()
	hours := numberParam(params, "hours", 4)
	taskID := int64(numberParam(params, "task_id", 0))
	nodeID := stringParam(params, "uuid")
	if nodeID != "" {
		if err := a.ensureVisibleNode(ctx, nodeID, authenticated); err != nil {
			return nil, err
		}
	}
	records, err := a.store.ProbeHistory(ctx, nodeID, taskID, end.Add(-time.Duration(hours*float64(time.Hour))), end, int(numberParam(params, "maxCount", 4000)))
	if err != nil {
		return nil, err
	}
	records, err = a.filterVisibleProbeRecords(ctx, records, authenticated)
	if err != nil {
		return nil, err
	}
	return a.komariProbeRecords(ctx, records, authenticated), nil
}
