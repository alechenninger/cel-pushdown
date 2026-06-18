# cel-pushdown

`cel-pushdown` compiles a CEL predicate into a native backend query plan plus residual CEL predicates.

The prototype focuses on Kubernetes. It pushes the parts Kubernetes can execute natively, such as GVK, namespace, name, labels, and known field selectors, then preserves everything else as residual CEL evaluated on the returned candidates.

## Why

Native backends often have indexes or selectors that can cheaply reduce the candidate set. CEL is more expressive than those selectors, so the useful split is:

```text
original CEL predicate
  -> one or more native backend searches
  -> each search has pushed-down constraints
  -> each search may have residual CEL
  -> executing the searches returns candidates
  -> residual CEL filtering returns final matches
```

Correctness is the main goal. Native searches may over-select, but they must not under-select:

```text
if an object satisfies the original CEL expression,
it must be returned by at least one native search before residual filtering.
```

Unsupported or uncertain predicates stay residual.

## Generic Shape

The core API uses Go generics for the native query type:

- `Plan[Q]`: a backend plan for a strongly typed native query.
- `Search[Q]`: one native query plus optional residual CEL.
- `CELPredicate`: checked CEL AST plus executable CEL program.
- `Candidate[V]`: a value returned by a backend search.
- `BackendPlanner[Q]`: plans an expression for one backend.
- `BackendExecutor[Q,V]`: executes a typed backend plan.

Planning expands ordinary `&&` and `||` expressions into DNF branches up to `MaxBranches` and asks the backend to push down each branch. If expansion would exceed the cap, planning returns an error instead of producing an unsafe approximation.

## Kubernetes Support

The Kubernetes backend plans CEL over a variable named `object`, represented as unstructured Kubernetes data.

Supported pushdowns include:

- `object.apiVersion` and `object.kind` to `schema.GroupVersionKind`.
- Optional RESTMapper resolution from GVK to GVR.
- `object.metadata.namespace == "prod"`.
- `object.metadata.name == "web"`.
- Label selectors:
  - `object.metadata.labels["app"] == "web"`
  - `object.metadata.labels["app"] != "web"`
  - `object.metadata.labels["app"] in ["web", "api"]`
  - `!(object.metadata.labels["app"] in ["web", "api"])`
  - `"app" in object.metadata.labels`
  - `!("app" in object.metadata.labels)`
- Known field selectors:
  - `metadata.name`
  - `metadata.namespace`
  - built-in Pod fields such as `spec.nodeName` and `status.phase`
  - configured static fields for other resources or CRDs
- `OR` via multiple searches.
- Multiple GVKs in one plan.
- Per-search residual CEL.

The executor uses a Kubernetes dynamic client and runs searches serially. It supports `Get` when GVR, namespace, and name are known, otherwise `List` with label and field selectors. If a query has no resolved GVR, execution returns a clear error. If a namespaced resource has no namespace and all-namespaces listing is disabled, execution returns a clear error.

Results are deduplicated by:

```text
group/version/kind + namespace + name
```

## Examples

### Pod Label Search With Residual

```cel
object.apiVersion == "v1" &&
object.kind == "Pod" &&
object.metadata.namespace == "default" &&
object.metadata.labels["app"] == "web" &&
object.metadata.name.startsWith("web-")
```

Plan:

- one Pod search in namespace `default`
- label selector `app=web`
- residual `object.metadata.name.startsWith("web-")`
- partial search

### Factored Multiple GVKs

```cel
object.metadata.labels["app"] == "web" &&
(
  (object.apiVersion == "v1" && object.kind == "Pod") ||
  (object.apiVersion == "apps/v1" && object.kind == "Deployment")
)
```

Plan:

- one Pod search with `app=web`
- one Deployment search with `app=web`
- no residual if both branches are fully pushed

### Namespace Set

```cel
object.metadata.namespace in ["prod", "stage"] &&
object.apiVersion == "v1" &&
object.kind == "Pod"
```

Plan:

- one Pod search in namespace `prod`
- one Pod search in namespace `stage`

## Minimal Usage

```go
planner := kubernetes.NewPlanner(kubernetes.WithRESTMapper(mapper))

plan, err := planner.Plan(ctx, `
  object.apiVersion == "v1" &&
  object.kind == "Pod" &&
  object.metadata.labels["app"] == "web" &&
  object.metadata.name.startsWith("web-")
`)
if err != nil {
  return err
}

executor := &kubernetes.KubernetesExecutor{
  Client: client,
  Mapper: mapper,
  AllowAllNamespaces: true,
}

matches, err := executor.ExecuteAndFilter(ctx, plan)
```

There is also a small demo CLI:

```sh
go run ./cmd/cel-pushdown-plan --expr 'object.apiVersion == "v1" && object.kind == "Pod" && object.metadata.labels["app"] == "web"'
```

## Limitations

- Not all CEL can be pushed down.
- Branch expansion is capped.
- Field selector support must be known or configured; unsupported fields remain residual.
- Execution is serial.
- Watch is not implemented.
- There are no cross-resource joins.
- The planner does not translate arbitrary CEL functions into Kubernetes selectors.
- The Kubernetes backend does not discover every resource automatically.
- It does not use arbitrary Kubernetes server-side CEL.
- Not every expression is guaranteed to be plannable for execution, especially without GVK/GVR information.

## Non-goals

- Perfect CEL rewriting.
- Full query optimization.
- Parallel execution.
- Watch support.
- Discovering every Kubernetes resource automatically.
- Pushing unsupported fields.
- Translating arbitrary CEL functions into Kubernetes selectors.
- Guaranteeing that every expression is executable.

## Future Work

- Parallel execution.
- Query deduplication and merging.
- Watch support.
- CRD `selectableFields` discovery.
- OpenAPI/schema-aware CEL typing.
- Cost-based planning.
- Additional backends such as SQL, Elasticsearch, and in-memory indexes.
- Better use of cel-go residual ASTs.
- More advanced OR simplification.
- Plan explain output.
