# cel-pushdown

`cel-pushdown` is a Go prototype for predicate pushdown from a restricted subset of CEL over Kubernetes objects into native Kubernetes list/get queries.

It compiles the Kubernetes-query-compatible subset of CEL into Kubernetes list/get options and leaves the rest as residual CEL.

## Safety and correctness

The planner only pushes constraints that Kubernetes can enforce without losing valid matches.

- The pushed-down Kubernetes query is an **over-approximation** of the original expression.
- False positives are allowed before client-side filtering.
- False negatives are not allowed.
- Residual CEL removes false positives after Kubernetes returns candidate objects.

## Concepts

- **Original expression**: the full user-provided CEL predicate.
- **Pushed-down constraints**: the subset translated into Kubernetes-native query parameters.
- **Residual expression**: the remaining CEL predicate that must still be evaluated client-side.
- **Exact plan**: no residual expression remains.
- **Partial plan**: some Kubernetes query constraints were extracted and residual CEL remains.
- **Unpushable plan**: no useful Kubernetes query constraints were extracted.

## Example

Input CEL:

```cel
object.apiVersion == "v1" &&
object.kind == "Pod" &&
object.metadata.namespace == "prod" &&
object.metadata.labels["app"] in ["web", "api"] &&
object.spec.nodeName == "node-a" &&
object.metadata.name.startsWith("web-")
```

Typical rendered plan:

```text
resource:
  group: ""
  version: "v1"
  kind: "Pod"
  resource: "pods"
scope:
  namespace: "prod"
selectors:
  labelSelector: "app in (web,api)"
  fieldSelector: "spec.nodeName=node-a"
residual: object.metadata.name.startsWith("web-")
exact: false
```

Code sample:

```go
planner, err := celpushdown.NewPlanner(celpushdown.PlanOptions{
    AllowAllNamespaces: true,
    FieldSupport:       celpushdown.BuiltinFieldSupport{},
    ResourceResolver:   resolver,
    PreserveOriginalCEL: true,
})
if err != nil {
    return err
}

plan, err := planner.Plan(ctx, `object.apiVersion == "v1" &&
object.kind == "Pod" &&
object.metadata.namespace == "default" &&
object.metadata.labels["app"] == "web" &&
object.metadata.name.startsWith("web-")`)
if err != nil {
    return err
}

items, err := planner.ExecuteAndFilter(ctx, dynamicClient, plan)
if err != nil {
    return err
}
_ = items
```

`Execute` performs only the Kubernetes query. `ExecuteAndFilter` additionally evaluates the residual expression against returned `unstructured.Unstructured` objects.

## Package overview

The package lives at the repository root as `package celpushdown`.

Main API:

```go
type Planner struct {}

type Plan struct {
    OriginalExpression string
    Resource           ResourcePlan
    Scope              ScopePlan
    Selectors          SelectorPlan
    Residual           *ResidualPlan
    Exact              bool
    Warnings           []Warning
}

func NewPlanner(opts PlanOptions) (*Planner, error)
func (p *Planner) Plan(ctx context.Context, expr string) (*Plan, error)
func (p *Planner) Execute(ctx context.Context, client dynamic.Interface, plan *Plan) ([]unstructured.Unstructured, error)
func (p *Planner) ExecuteAndFilter(ctx context.Context, client dynamic.Interface, plan *Plan) ([]unstructured.Unstructured, error)
func (p *Planner) EvalResidual(ctx context.Context, plan *Plan, obj map[string]any) (bool, error)
```

## Supported pushdowns

### Resource selection

- `object.apiVersion == "v1"`
- `object.apiVersion == "apps/v1"`
- `object.kind == "Pod"`
- `object.kind == "Deployment"`

When both `apiVersion` and `kind` are known, the planner can resolve a `GroupVersionResource` through a configured `ResourceResolver`.

### Scope

- `object.metadata.namespace == "prod"`
- `object.metadata.name == "web"`

If resource, namespace, and name are all known, execution uses `Get`. Otherwise name equality is also pushed as a field selector when field support allows it.

### Labels

- `object.metadata.labels["app"] == "web"`
- `object.metadata.labels["app"] != "web"`
- `object.metadata.labels["app"] in ["web", "api"]`
- `!(object.metadata.labels["app"] in ["web", "api"])`
- `"app" in object.metadata.labels`
- `!("app" in object.metadata.labels)`

These become Kubernetes label selector requirements such as:

- `app=web`
- `app!=web`
- `app in (web,api)`
- `app notin (web,api)`
- `app`
- `!app`

### Field selectors

Supported only when `FieldSupport` allows the path.

Included helpers:

- `MinimalFieldSupport`: `metadata.name`, `metadata.namespace`
- `BuiltinFieldSupport`: minimal fields plus Pod `spec.nodeName` and `status.phase`
- `StaticFieldSupport`: caller-configured global and per-GVK fields

Examples:

- `object.status.phase == "Running"`
- `object.spec.nodeName == "node-a"`
- `object.spec.replicas == 3` only when explicitly configured

## Residual handling

Unsupported CEL remains in the residual expression.

Examples that stay residual:

- `object.metadata.name.startsWith("web-")`
- `object.metadata.creationTimestamp < timestamp("2026-01-01T00:00:00Z")`
- `object.spec.containers.exists(c, c.image.contains("nginx"))`
- `size(object.metadata.labels) > 3`
- `object.spec.replicas > 3`
- `object.metadata.labels["a"] == object.metadata.labels["b"]`

The current prototype preserves residuals by splitting top-level `&&` conjuncts, pushing supported conjuncts, and re-joining the rest.

## Planning behavior

- `&&` is the main extraction path.
- Unsupported `||` remains residual.
- `!` is supported only for label existence and label membership cases with direct Kubernetes selector equivalents.
- The planner does not guess a resource when only `kind` is known.
- The executor will not query unresolved resources.
- All-namespaces execution is blocked unless `AllowAllNamespaces` is enabled.

## Non-goals

- Full CEL-to-Kubernetes compilation
- Arbitrary OR planning
- Arbitrary function translation
- Cross-resource joins
- Server-side CEL evaluation
- Perfect SQL-like optimization

## Future work

- Multi-query plans for OR
- Watch support
- CRD `selectableFields` discovery
- OpenAPI/schema-aware field validation
- Cost modeling
- Better residual AST rewriting using cel-go residual APIs
- Additional backend implementations such as SQL or search systems

## Development

Run tests with:

```bash
go test ./...
```
