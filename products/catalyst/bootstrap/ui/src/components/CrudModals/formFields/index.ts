/**
 * formFields/index.ts — barrel exporting every typed form-fields
 * component used by Add and Edit modals. One schema per kind so Add
 * and Edit cannot drift.
 */

export { RegionFormFields, type RegionFormValues, type RegionFormFieldsProps } from './RegionFormFields'
export { ClusterFormFields, type ClusterFormValues, type ClusterFormFieldsProps } from './ClusterFormFields'
export { VClusterFormFields, type VClusterFormValues, type VClusterFormFieldsProps } from './VClusterFormFields'
export { NodePoolFormFields, type NodePoolFormValues, type NodePoolFormFieldsProps } from './NodePoolFormFields'
export { WorkerNodeFormFields, type WorkerNodeFormValues, type WorkerNodeFormFieldsProps } from './WorkerNodeFormFields'
export { LoadBalancerFormFields, type LoadBalancerFormValues, type LoadBalancerFormFieldsProps } from './LoadBalancerFormFields'
export { parseLBPorts } from './parseLBPorts'
export { NetworkFormFields, type NetworkFormValues, type NetworkFormFieldsProps } from './NetworkFormFields'
export { PVCFormFields, type PVCFormValues, type PVCFormFieldsProps } from './PVCFormFields'
export { BucketFormFields, type BucketFormValues, type BucketFormFieldsProps } from './BucketFormFields'
export { VolumeFormFields, type VolumeFormValues, type VolumeFormFieldsProps } from './VolumeFormFields'
