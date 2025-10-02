package excel

import (
	"context"
	"fmt"
	"sync"

	"github.com/xll-gen/xll-gen/xloper"
	"golang.org/x/sync/singleflight"
)

// Cacher defines an interface for a simple key-value cache. It is used by the
// Context to store results of expensive operations like `SheetId` or `Coerce`
// within a single calculation cycle. A `sync.Map` is a suitable implementation.
type Cacher interface {
	Load(key any) (value any, ok bool)
	Store(key, value any)
	Delete(key any)
	Clear()
}

// Context provides a request-scoped environment for Excel operations. It manages
// state for a single calculation cycle, providing caching, request coalescing
// via `singleflight`, and cancellation. When a calculation cycle ends, the
// context is reset.
type Context struct {
	ctx        context.Context
	cancelFunc context.CancelFunc
	excel      *Excel
	baseCtx    context.Context
	group      singleflight.Group
	cache      Cacher
}

// Ctx returns the underlying `context.Context` for the current calculation cycle.
// This context is canceled when the calculation ends.
func (c *Context) Ctx() context.Context {
	return c.ctx
}

// Cancel explicitly cancels the context for the current calculation cycle.
func (c *Context) Cancel() {
	c.cancelFunc()
}

// Excel returns the underlying `*Excel` instance for making direct calls to the API.
func (c *Context) Excel() *Excel {
	return c.excel
}

// Cache returns the Cacher instance associated with this context.
func (c *Context) Cache() Cacher {
	return c.cache
}

// Close cancels the context and closes the underlying Excel connection, unregistering
// all functions and event handlers.
func (c *Context) Close() error {
	c.cancelFunc()
	return c.Excel().Close()
}

// SheetId gets the sheet ID for a given sheet name. It uses a singleflight group
// to coalesce concurrent requests for the same sheet name within a calculation cycle,
// preventing redundant calls to Excel.
func (c *Context) SheetId(sheetNm string) (uintptr, error) {
	if sheetNm == "" {
		return c.excel.SheetId(sheetNm)
	}

	res, err, _ := c.group.Do(fmt.Sprintf("sheetId::%s", sheetNm), func() (any, error) {
		return c.excel.SheetId(sheetNm)
	})

	resUintPtr, ok := res.(uintptr)
	if !ok {
		return 0, fmt.Errorf("expected uintptr type for sheet ID, got %T", res)
	}
	return resUintPtr, err
}

// SheetName gets the sheet name for a given sheet ID. It uses a singleflight group
// to coalesce concurrent requests for the same sheet ID within a calculation cycle,
// preventing redundant calls to Excel.
func (c *Context) SheetName(idSheet uintptr) (string, error) {
	if idSheet == 0 {
		return c.excel.SheetName(idSheet)
	}

	res, err, _ := c.group.Do(fmt.Sprintf("sheetName::%d", idSheet), func() (any, error) {
		return c.excel.SheetName(idSheet)
	})

	resString, ok := res.(string)
	if !ok {
		return "", fmt.Errorf("expected string type for sheet name, got %T", res)
	}

	return resString, err
}

// Coerce retrieves the value(s) from a given reference. It uses a singleflight
// group to coalesce concurrent requests for the same reference within a calculation
// cycle, preventing redundant calls to Excel.
func (c *Context) Coerce(r xloper.Ref) (any, error) {
	if r == nil {
		return nil, nil
	}

	res, err, _ := c.group.Do(fmt.Sprintf("coerce::%+v", r), func() (any, error) {
		return c.excel.Coerce(r)
	})

	return res, err
}

// CalculationEnded is an event handler that resets the context's state at the
// end of an Excel calculation cycle. It clears the singleflight group and cache,
// and renews the underlying cancellable context.
func (c *Context) CalculationEnded() {
	c.group = singleflight.Group{}
	c.cancelFunc()
	c.ctx, c.cancelFunc = context.WithCancel(c.baseCtx)
	c.cache.Clear()
}

// Caller returns the range of the cell that called the current user-defined function.
func (c *Context) Caller() Range {
	r, err := c.excel.Call(XlfCaller)
	if err != nil {
		return Range{}
	}
	return r.(Range)
}

// ContextOptions provides optional configuration for creating a new Context.
type ContextOptions struct {
	// BaseCtx is the parent context. If nil, context.Background() is used.
	BaseCtx context.Context
	// Cache is the caching implementation. If nil, a new `sync.Map` is used.
	Cache Cacher
}

// NewContext creates and initializes a new Context with the given Excel instance and options.
func NewContext(e *Excel, options ...ContextOptions) *Context {
	var opt ContextOptions
	if len(options) > 0 {
		opt = options[0]
	} else {
		opt = ContextOptions{}
	}

	var baseCtx context.Context

	if opt.BaseCtx != nil {
		baseCtx = opt.BaseCtx
	} else {
		baseCtx = context.Background()
	}

	var ctx context.Context
	ctx, cancel := context.WithCancel(baseCtx)

	var cache Cacher
	if opt.Cache != nil {
		cache = opt.Cache
	} else {
		cache = &sync.Map{}
	}

	ret := &Context{
		baseCtx:    baseCtx,
		cancelFunc: cancel,
		excel:      e,
		ctx:        ctx,
		group:      singleflight.Group{},
		cache:      cache,
	}

	e.RegisterEventHandler(XlEventCalculationEnded, ret.CalculationEnded)

	return ret
}
