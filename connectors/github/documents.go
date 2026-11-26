package main

// Every GraphQL document the Connector sends is a fixed text: the host admits
// a POST /graphql body only when its query is byte-identical to one the Route
// declares, so a value (a label name, an id) travels in variables, never in
// the text. routes.nix declares the same texts and routes_test.go pins the two.
const (
	markReadyForReviewMutation = `mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){clientMutationId}}`
	convertToDraftMutation     = `mutation($id:ID!){convertPullRequestToDraft(input:{pullRequestId:$id}){clientMutationId}}`
	enableAutoMergeMutation    = `mutation($id:ID!){enablePullRequestAutoMerge(input:{pullRequestId:$id,mergeMethod:REBASE}){clientMutationId}}`
	issueIDQuery               = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){issue(number:$number){id}}}`
	labelIDQuery               = `query($owner:String!,$repo:String!,$number:Int!,$name:String!){repository(owner:$owner,name:$repo){issue(number:$number){id} label(name:$name){id}}}`
	removeLabelsMutation       = `mutation($id:ID!,$ids:[ID!]!){removeLabelsFromLabelable(input:{labelableId:$id,labelIds:$ids}){clientMutationId}}`
	closeIssueMutation         = `mutation($id:ID!){closeIssue(input:{issueId:$id,stateReason:COMPLETED}){clientMutationId}}`
)

// documents names each document for the Route that declares it and the host's
// audit that reports it.
var documents = map[string]string{
	"rollup":             rollupQuery,
	"comments":           commentsQuery,
	"repository":         repoQuery,
	"viewer":             viewerQuery,
	"markReadyForReview": markReadyForReviewMutation,
	"convertToDraft":     convertToDraftMutation,
	"enableAutoMerge":    enableAutoMergeMutation,
	"issueID":            issueIDQuery,
	"labelID":            labelIDQuery,
	"removeLabels":       removeLabelsMutation,
	"closeIssue":         closeIssueMutation,
}
