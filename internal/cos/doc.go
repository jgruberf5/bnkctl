// Package cos wraps IBM/ibm-cos-sdk-go for COS bucket and object I/O.
// The SDK is S3-compatible (literally a fork of AWS S3 SDK with IBM IAM
// auth bolted in), so the operations look familiar:
//
//   - Client.{Create,Delete,List}Bucket
//   - Client.{Put,Get,Delete,List}Object…
//
// Pattern: one Client per (region, COS instance CRN) pair. The IAM
// credentials inside the client are scoped to that single instance, so
// switching instances means a fresh Client.
//
// COS instance CRUD (create the instance itself) lives in package ibm —
// instances are generic IBM Cloud resources managed by Resource
// Controller, not S3.
package cos
