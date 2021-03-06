package dotweb

import (
	"devfeel/dotweb/framework/convert"
	"devfeel/dotweb/framework/exception"
	"devfeel/dotweb/framework/json"
	"devfeel/dotweb/framework/log"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/julienschmidt/httprouter"
	"golang.org/x/net/websocket"
)

type (
	//HttpModule定义
	HttpModule struct {
		//响应请求时作为 HTTP 执行管线链中的第一个事件发生
		OnBeginRequest func(*HttpContext)
		//响应请求时作为 HTTP 执行管线链中的最后一个事件发生。
		OnEndRequest func(*HttpContext)
	}

	//HttpServer定义
	HttpServer struct {
		router *httprouter.Router
		dotweb *Dotweb
		pool   *pool
	}

	//pool定义
	pool struct {
		response sync.Pool
		context  sync.Pool
	}
)

// Handle is a function that can be registered to a route to handle HTTP
// requests. Like http.HandlerFunc, but has a third parameter for the values of
// wildcards (variables).
type HttpHandle func(*HttpContext)

func NewHttpServer() *HttpServer {
	server := &HttpServer{
		router: httprouter.New(),
		pool: &pool{
			response: sync.Pool{
				New: func() interface{} {
					return &Response{}
				},
			},
			context: sync.Pool{
				New: func() interface{} {
					return &HttpContext{}
				},
			},
		},
	}
	return server
}

//ServeHTTP makes the httprouter implement the http.Handler interface.
func (server *HttpServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	//针对websocket与调试信息特殊处理
	if checkIsWebSocketRequest(req) {
		http.DefaultServeMux.ServeHTTP(w, req)
	} else {
		//设置header信息
		w.Header().Set(HeaderServer, DefaultServerName)
		server.router.ServeHTTP(w, req)
	}
}

/*
* 关联当前HttpServer实例对应的DotServer实例
 */
func (server *HttpServer) setDotweb(dotweb *Dotweb) {
	server.dotweb = dotweb
}

// GET is a shortcut for router.Handle("GET", path, handle)
func (server *HttpServer) GET(path string, handle HttpHandle) {
	server.router.Handle("GET", path, server.wrapRouterHandle(handle, false))
}

// HEAD is a shortcut for router.Handle("HEAD", path, handle)
func (server *HttpServer) HEAD(path string, handle HttpHandle) {
	server.router.Handle("HEAD", path, server.wrapRouterHandle(handle, false))
}

// OPTIONS is a shortcut for router.Handle("OPTIONS", path, handle)
func (server *HttpServer) OPTIONS(path string, handle HttpHandle) {
	server.router.Handle("OPTIONS", path, server.wrapRouterHandle(handle, false))
}

// POST is a shortcut for router.Handle("POST", path, handle)
func (server *HttpServer) POST(path string, handle HttpHandle) {
	server.router.Handle("POST", path, server.wrapRouterHandle(handle, false))
}

// PUT is a shortcut for router.Handle("PUT", path, handle)
func (server *HttpServer) PUT(path string, handle HttpHandle) {
	server.router.Handle("PUT", path, server.wrapRouterHandle(handle, false))
}

// PATCH is a shortcut for router.Handle("PATCH", path, handle)
func (server *HttpServer) PATCH(path string, handle HttpHandle) {
	server.router.Handle("PATCH", path, server.wrapRouterHandle(handle, false))
}

// DELETE is a shortcut for router.Handle("DELETE", path, handle)
func (server *HttpServer) DELETE(path string, handle HttpHandle) {
	server.router.Handle("DELETE", path, server.wrapRouterHandle(handle, false))
}

// DELETE is a shortcut for router.Handle("DELETE", path, handle)
func (server *HttpServer) HiJack(path string, handle HttpHandle) {
	server.router.Handle("GET", path, server.wrapRouterHandle(handle, true))
}

// ServerFile is a shortcut for router.ServeFiles(path, filepath)
// simple demo:server.ServerFile("/src/*filepath", "/var/www")
func (server *HttpServer) ServerFile(urlpath string, filepath string) {
	server.router.ServeFiles(urlpath, http.Dir(filepath))
}

// WebSocket is a shortcut for websocket.Handler
func (server *HttpServer) WebSocket(path string, handle HttpHandle) {
	http.Handle(path, websocket.Handler(server.wrapWebSocketHandle(handle)))
}

type LogJson struct {
	RequestUrl string
	HttpHeader string
	HttpBody   string
}

//wrap HttpHandle to httprouter.Handle
func (server *HttpServer) wrapRouterHandle(handle HttpHandle, isHijack bool) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		//get from pool
		res := server.pool.response.Get().(*Response)
		res.Reset(w)
		httpCtx := server.pool.context.Get().(*HttpContext)
		httpCtx.Reset(res, r, params)

		//hijack处理
		if isHijack {
			_, hijack_err := httpCtx.Hijack()
			if hijack_err != nil {
				//输出内容
				httpCtx.Response.WriteHeader(http.StatusInternalServerError)
				httpCtx.Response.Header().Set(HeaderContentType, CharsetUTF8)
				httpCtx.WriteString(hijack_err.Error())
			}
		}

		startTime := time.Now()
		defer func() {
			var errmsg string
			if err := recover(); err != nil {
				errmsg = exception.CatchError("httpserver::RouterHandle", LogTarget_HttpServer, err)
				//具体异常处理函数
				if server.dotweb.ExceptionHandler != nil {
					server.dotweb.ExceptionHandler(httpCtx, err)
				} else {
					//输出内容
					httpCtx.Response.WriteHeader(http.StatusInternalServerError)
					httpCtx.Response.Header().Set(HeaderContentType, CharsetUTF8)
					io.WriteString(httpCtx.Response.writer, errmsg)
				}

				//记录访问日志
				headinfo := fmt.Sprintln(httpCtx.Response.Header)
				logJson := LogJson{
					RequestUrl: httpCtx.Request.RequestURI,
					HttpHeader: headinfo,
					HttpBody:   errmsg,
				}
				logString := jsonutil.GetJsonString(logJson)
				logger.Log(logString, LogTarget_HttpServer, LogLevel_Error)

				//增加错误计数
				GlobalState.AddErrorCount(1)
			}
			timetaken := int64(time.Now().Sub(startTime) / time.Millisecond)
			//HttpServer Logging
			logger.Log(httpCtx.Url()+" "+logString(httpCtx, timetaken), LogTarget_HttpRequest, LogLevel_Debug)

			// Return to pool
			server.pool.response.Put(res)
			server.pool.context.Put(httpCtx)
		}()

		//处理前置Module集合
		for _, module := range server.dotweb.Modules {
			if module.OnBeginRequest != nil {
				module.OnBeginRequest(httpCtx)
			}
		}

		//处理用户handle
		handle(httpCtx)

		//处理后置Module集合
		for _, module := range server.dotweb.Modules {
			if module.OnEndRequest != nil {
				module.OnEndRequest(httpCtx)
			}
		}

		//增加状态计数
		GlobalState.AddRequestCount(1)
	}
}

//wrap HttpHandle to websocket.Handle
func (server *HttpServer) wrapWebSocketHandle(handle HttpHandle) websocket.Handler {
	return func(ws *websocket.Conn) {
		//get from pool
		httpCtx := server.pool.context.Get().(*HttpContext)
		httpCtx.Reset(nil, ws.Request(), nil)
		httpCtx.WebSocket = &WebSocket{
			Conn: ws,
		}
		httpCtx.IsWebSocket = true

		startTime := time.Now()
		defer func() {
			var errmsg string
			if err := recover(); err != nil {
				errmsg = exception.CatchError("httpserver::WebsocketHandle", LogTarget_HttpServer, err)

				//记录访问日志
				headinfo := fmt.Sprintln(httpCtx.WebSocket.Conn.Request().Header)
				logJson := LogJson{
					RequestUrl: httpCtx.WebSocket.Conn.Request().RequestURI,
					HttpHeader: headinfo,
					HttpBody:   errmsg,
				}
				logString := jsonutil.GetJsonString(logJson)
				logger.Log(logString, LogTarget_HttpServer, LogLevel_Error)

				//增加错误计数
				GlobalState.AddErrorCount(1)
			}
			timetaken := int64(time.Now().Sub(startTime) / time.Millisecond)
			//HttpServer Logging
			logger.Log(httpCtx.Url()+" "+logString(httpCtx, timetaken), LogTarget_HttpRequest, LogLevel_Debug)

			// Return to pool
			server.pool.context.Put(httpCtx)
		}()

		handle(httpCtx)

		//增加状态计数
		GlobalState.AddRequestCount(1)
	}
}

//get default log string
func logString(ctx *HttpContext, timetaken int64) string {

	var reqbytelen, resbytelen, method, proto, status, userip string
	if !ctx.IsWebSocket {
		reqbytelen = convert.Int642String(ctx.Request.ContentLength)
		resbytelen = convert.Int642String(ctx.Response.Size)
		method = ctx.Request.Method
		proto = ctx.Request.Proto
		status = convert.Int2String(ctx.Response.Status)
		userip = ctx.RemoteIP()
	} else {
		reqbytelen = convert.Int642String(ctx.Request.ContentLength)
		resbytelen = "0"
		method = ctx.Request.Method
		proto = ctx.Request.Proto
		status = "0"
		userip = ctx.RemoteIP()
	}

	log := method + " "
	log += userip + " "
	log += proto + " "
	log += status + " "
	log += reqbytelen + " "
	log += resbytelen + " "
	log += convert.Int642String(timetaken)

	return log
}

//check request is the websocket request
func checkIsWebSocketRequest(req *http.Request) bool {
	if req.Header.Get("Connection") == "Upgrade" {
		return true
	}
	return false
}

//check request is startwith /debug/
func checkIsDebugRequest(req *http.Request) bool {
	if strings.Index(req.RequestURI, "/debug/") == 0 {
		return true
	}
	return false
}
