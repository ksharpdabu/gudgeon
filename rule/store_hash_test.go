package rule

import (
	"testing"
)

func TestHashRuleStore(t *testing.T) {
	testStore(defaultRuleData, func() RuleStore { return &hashStore{} }, t)
}

func BenchmarkHashRuleStore(b *testing.B) {
	benchNonComplexStore(func() RuleStore { return &hashStore{} }, b)
}

func TestHashSqlRuleStore(t *testing.T) {
	testStore(defaultRuleData, func() RuleStore {
		return &hashStore{
			delegate: &sqlStore{},
		}
	}, t)
}

func BenchmarkHashSqlRuleStore(b *testing.B) {
	benchNonComplexStore(func() RuleStore {
		return &hashStore{
			delegate: &sqlStore{},
		}
	}, b)
}
