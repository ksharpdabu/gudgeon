package resolver

import (
	"github.com/miekg/dns"

	"github.com/chrisruffalo/gudgeon/util"
)

type resolverSource struct {
	resolverName string
}

func newResolverSource(sourceName string) Source {
	if "" == sourceName {
		return nil
	}

	resolver := new(resolverSource)
	resolver.resolverName = sourceName
	return resolver
}

func (resolverSource *resolverSource) Name() string {
	return resolverSource.resolverName
}

func (resolverSource *resolverSource) Answer(context *ResolutionContext, request *dns.Msg) (*dns.Msg, error) {
	// bail if context is nil or resolver map is not available
	if context == nil || context.ResolverMap == nil {
		return nil, nil
	}

	// check that the target resolver has not already been visited
	// and return if it has been
	if util.StringIn(resolverSource.resolverName, context.Visited) {
		return nil, nil
	}

	// continue resolution chain
	return context.ResolverMap.answerWithContext(resolverSource.resolverName, context, request)
}
