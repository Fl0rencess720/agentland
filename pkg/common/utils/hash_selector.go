package utils

import (
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

func SelectorWithHashValue(key, name string) (labels.Selector, error) {
	req, err := labels.NewRequirement(key, selection.Equals, []string{NameHash(name)})
	if err != nil {
		return nil, err
	}
	return labels.NewSelector().Add(*req), nil
}

func AddHashValueRequirement(selector labels.Selector, key, name string) (labels.Selector, error) {
	req, err := labels.NewRequirement(key, selection.Equals, []string{NameHash(name)})
	if err != nil {
		return nil, err
	}
	return selector.Add(*req), nil
}
