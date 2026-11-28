// Package rules contains the rule domain model and its validation.
package rules

import (
	"context"
	"errors"
	"fmt"

	"github.com/atpanos/docktimer/internal/database"
)

// Service implements rule CRUD on top of the store.
type Service struct {
	store *database.Store
}

// NewService creates a rule service.
func NewService(store *database.Store) *Service {
	return &Service{store: store}
}

// List returns all rules.
func (s *Service) List(ctx context.Context) ([]database.Rule, error) {
	return s.store.ListRules(ctx)
}

// Get returns a single rule.
func (s *Service) Get(ctx context.Context, id int64) (database.Rule, error) {
	return s.store.RuleByID(ctx, id)
}

// Create validates and stores a new rule.
func (s *Service) Create(ctx context.Context, in Input, createdBy *int64) (database.Rule, error) {
	if err := Validate(&in); err != nil {
		return database.Rule{}, err
	}
	return s.store.CreateRule(ctx, database.Rule{
		TargetType:     in.TargetType,
		TargetRef:      in.TargetRef,
		TargetName:     in.TargetName,
		Action:         in.Action,
		CronExpression: in.Cron,
		Timezone:       in.Timezone,
		Enabled:        in.Enabled,
		CreatedBy:      createdBy,
	})
}

// Update validates and updates an existing rule.
func (s *Service) Update(ctx context.Context, id int64, in Input) (database.Rule, error) {
	if err := Validate(&in); err != nil {
		return database.Rule{}, err
	}
	existing, err := s.store.RuleByID(ctx, id)
	if err != nil {
		return database.Rule{}, err
	}
	existing.TargetType = in.TargetType
	existing.TargetRef = in.TargetRef
	existing.TargetName = in.TargetName
	existing.Action = in.Action
	existing.CronExpression = in.Cron
	existing.Timezone = in.Timezone
	existing.Enabled = in.Enabled
	if err := s.store.UpdateRule(ctx, existing); err != nil {
		return database.Rule{}, err
	}
	return s.store.RuleByID(ctx, id)
}

// Toggle enables or disables a rule and returns the updated rule.
func (s *Service) Toggle(ctx context.Context, id int64, enabled bool) (database.Rule, error) {
	if err := s.store.SetRuleEnabled(ctx, id, enabled); err != nil {
		return database.Rule{}, err
	}
	return s.store.RuleByID(ctx, id)
}

// Delete removes a rule.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := s.store.DeleteRule(ctx, id); err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return database.ErrNotFound
		}
		return fmt.Errorf("delete rule: %w", err)
	}
	return nil
}
