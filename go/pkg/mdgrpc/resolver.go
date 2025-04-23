// Minidisc gRPC address resolver.
//
// This custom resolver allows using addresses of the form
//     minidisc://name
// or if you use labels
//     minidisc://name?label1=value1&label2=value2
//
// To use, just call mdgrpc.RegisterResolver() before creating any gRPC client
// connections.

package mdgrpc

import (
	"github.com/mscheidegger/minidisc/go/pkg/minidisc"
	"google.golang.org/grpc/resolver"
)

func RegisterResolver() {
	resolver.Register(&minidiscResolverBuilder{})
}

type minidiscResolverBuilder struct {
	resolver.Builder
}

func (mrb *minidiscResolverBuilder) Build(
	tgt resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions,
) (resolver.Resolver, error) {
	name := tgt.URL.Host
	labels := make(map[string]string)
	q := tgt.URL.Query()
	for key, _ := range q {
		labels[key] = q.Get(key)
	}
	r := &minidiscResolver{
		name:       name,
		labels:     labels,
		clientConn: cc,
	}
	go func() {
		// Kick off first resolution at construction time. gRPC will apparently
		// only do it after this initial attempt.
		r.ResolveNow(resolver.ResolveNowOptions{})
	}()
	return r, nil
}

func (mrb *minidiscResolverBuilder) Scheme() string {
	return "minidisc"
}

type minidiscResolver struct {
	resolver.Resolver

	name       string
	labels     map[string]string
	clientConn resolver.ClientConn
}

func (mr *minidiscResolver) ResolveNow(_ resolver.ResolveNowOptions) {
	addr, err := minidisc.FindService(mr.name, mr.labels)
	if err != nil {
		mr.clientConn.ReportError(err)
		return
	}
	mr.clientConn.UpdateState(resolver.State{
		Endpoints: []resolver.Endpoint{
			resolver.Endpoint{
				Addresses: []resolver.Address{
					resolver.Address{Addr: addr.String()},
				},
			},
		},
	})
}

func (mr *minidiscResolver) Close() {
	// No-op
}
