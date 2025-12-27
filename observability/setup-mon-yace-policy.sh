#!/bin/bash
set -euo pipefail

# Setup IAM role and policies for the mon host:
# - YACE (Yet Another CloudWatch Exporter) - CloudWatch read access
# - OpenTelemetry Collector - S3 write access for logs
#
# This script is idempotent - safe to run multiple times.
#
# Reference: https://github.com/prometheus-community/yet-another-cloudwatch-exporter

REGION="us-west-2"
S3_LOGS_BUCKET="exe.dev-logs"
INSTANCE_ROLE_NAME="mon-instance-role"
INSTANCE_PROFILE_NAME="mon-instance-profile"
POLICY_NAME="yace-cloudwatch-access"
MACHINE_NAME="mon"

# Find the mon instance
echo "Finding instance ${MACHINE_NAME}..."
INSTANCE_ID=$(aws ec2 describe-instances \
    --filters "Name=tag:Name,Values=${MACHINE_NAME}" \
    "Name=instance-state-name,Values=running" \
    --query 'Reservations[].Instances[].InstanceId' \
    --output text \
    --region ${REGION})

if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" = "None" ]; then
    echo "ERROR: No running instance found with name ${MACHINE_NAME}"
    exit 1
fi

echo "Found instance: ${INSTANCE_ID}"

# Check if IAM role exists, create if not
echo "Checking IAM role ${INSTANCE_ROLE_NAME}..."
if ! aws iam get-role --role-name ${INSTANCE_ROLE_NAME} >/dev/null 2>&1; then
    echo "Creating IAM role ${INSTANCE_ROLE_NAME}..."
    aws iam create-role \
        --role-name ${INSTANCE_ROLE_NAME} \
        --assume-role-policy-document '{
            "Version": "2012-10-17",
            "Statement": [
                {
                    "Effect": "Allow",
                    "Principal": {"Service": "ec2.amazonaws.com"},
                    "Action": "sts:AssumeRole"
                }
            ]
        }'
else
    echo "IAM role ${INSTANCE_ROLE_NAME} already exists"
fi

# Create/update the YACE policy
# Reference: https://github.com/prometheus-community/yet-another-cloudwatch-exporter#authentication
echo "Creating/updating YACE policy ${POLICY_NAME}..."
aws iam put-role-policy \
    --role-name ${INSTANCE_ROLE_NAME} \
    --policy-name ${POLICY_NAME} \
    --policy-document '{
        "Version": "2012-10-17",
        "Statement": [
            {
                "Sid": "YACECorePermissions",
                "Effect": "Allow",
                "Action": [
                    "tag:GetResources",
                    "cloudwatch:GetMetricData",
                    "cloudwatch:GetMetricStatistics",
                    "cloudwatch:ListMetrics"
                ],
                "Resource": "*"
            },
            {
                "Sid": "YACEResourceDiscovery",
                "Effect": "Allow",
                "Action": [
                    "iam:ListAccountAliases",
                    "ec2:DescribeInstances",
                    "ec2:DescribeTransitGatewayAttachments",
                    "autoscaling:DescribeAutoScalingGroups",
                    "apigateway:GET",
                    "dms:DescribeReplicationInstances",
                    "dms:DescribeReplicationTasks"
                ],
                "Resource": "*"
            }
        ]
    }'
echo "Policy ${POLICY_NAME} applied to role ${INSTANCE_ROLE_NAME}"

# Create S3 bucket for logs if it doesn't exist
echo "Checking S3 bucket ${S3_LOGS_BUCKET}..."
if ! aws s3api head-bucket --bucket ${S3_LOGS_BUCKET} 2>/dev/null; then
    echo "Creating S3 bucket ${S3_LOGS_BUCKET}..."
    aws s3api create-bucket \
        --bucket ${S3_LOGS_BUCKET} \
        --region ${REGION} \
        --create-bucket-configuration LocationConstraint=${REGION}

    # Enable versioning for safety
    aws s3api put-bucket-versioning \
        --bucket ${S3_LOGS_BUCKET} \
        --versioning-configuration Status=Enabled

    # Set lifecycle policy to expire old logs after 90 days
    aws s3api put-bucket-lifecycle-configuration \
        --bucket ${S3_LOGS_BUCKET} \
        --lifecycle-configuration '{
            "Rules": [
                {
                    "ID": "ExpireOldLogs",
                    "Status": "Enabled",
                    "Filter": {},
                    "Expiration": {
                        "Days": 90
                    },
                    "NoncurrentVersionExpiration": {
                        "NoncurrentDays": 30
                    }
                }
            ]
        }'
    echo "S3 bucket ${S3_LOGS_BUCKET} created with 90-day expiration"
else
    echo "S3 bucket ${S3_LOGS_BUCKET} already exists"
fi

# Create/update S3 policy for otel-collector
echo "Creating/updating S3 policy for otel-collector..."
aws iam put-role-policy \
    --role-name ${INSTANCE_ROLE_NAME} \
    --policy-name "otel-collector-s3-logs" \
    --policy-document '{
        "Version": "2012-10-17",
        "Statement": [
            {
                "Sid": "OtelCollectorS3Write",
                "Effect": "Allow",
                "Action": [
                    "s3:PutObject",
                    "s3:GetObject",
                    "s3:ListBucket",
                    "s3:GetBucketLocation"
                ],
                "Resource": [
                    "arn:aws:s3:::'"${S3_LOGS_BUCKET}"'",
                    "arn:aws:s3:::'"${S3_LOGS_BUCKET}"'/*"
                ]
            }
        ]
    }'
echo "S3 policy applied to role ${INSTANCE_ROLE_NAME}"

# Check if instance profile exists, create if not
echo "Checking instance profile ${INSTANCE_PROFILE_NAME}..."
if ! aws iam get-instance-profile --instance-profile-name ${INSTANCE_PROFILE_NAME} >/dev/null 2>&1; then
    echo "Creating instance profile ${INSTANCE_PROFILE_NAME}..."
    aws iam create-instance-profile --instance-profile-name ${INSTANCE_PROFILE_NAME}

    echo "Adding role to instance profile..."
    aws iam add-role-to-instance-profile \
        --instance-profile-name ${INSTANCE_PROFILE_NAME} \
        --role-name ${INSTANCE_ROLE_NAME}

    # Wait for profile to be ready
    echo "Waiting for instance profile to propagate..."
    sleep 10
else
    echo "Instance profile ${INSTANCE_PROFILE_NAME} already exists"

    # Check if role is attached to profile
    ATTACHED_ROLE=$(aws iam get-instance-profile \
        --instance-profile-name ${INSTANCE_PROFILE_NAME} \
        --query 'InstanceProfile.Roles[0].RoleName' \
        --output text 2>/dev/null || echo "None")

    if [ "$ATTACHED_ROLE" = "None" ] || [ -z "$ATTACHED_ROLE" ]; then
        echo "Adding role to instance profile..."
        aws iam add-role-to-instance-profile \
            --instance-profile-name ${INSTANCE_PROFILE_NAME} \
            --role-name ${INSTANCE_ROLE_NAME}
        sleep 10
    elif [ "$ATTACHED_ROLE" != "${INSTANCE_ROLE_NAME}" ]; then
        echo "ERROR: Instance profile has different role attached: ${ATTACHED_ROLE}"
        exit 1
    else
        echo "Role already attached to instance profile"
    fi
fi

# Check if instance already has an IAM profile
CURRENT_PROFILE=$(aws ec2 describe-instances \
    --instance-ids ${INSTANCE_ID} \
    --query 'Reservations[0].Instances[0].IamInstanceProfile.Arn' \
    --output text \
    --region ${REGION})

if [ "$CURRENT_PROFILE" = "None" ] || [ -z "$CURRENT_PROFILE" ]; then
    echo "Attaching instance profile to ${MACHINE_NAME}..."
    aws ec2 associate-iam-instance-profile \
        --instance-id ${INSTANCE_ID} \
        --iam-instance-profile Name=${INSTANCE_PROFILE_NAME} \
        --region ${REGION}
    echo "Instance profile attached"
else
    CURRENT_PROFILE_NAME=$(basename "$CURRENT_PROFILE")
    if [ "$CURRENT_PROFILE_NAME" = "${INSTANCE_PROFILE_NAME}" ]; then
        echo "Instance already has correct profile attached: ${INSTANCE_PROFILE_NAME}"
    else
        echo "ERROR: Instance has different profile attached: ${CURRENT_PROFILE_NAME}"
        echo "To replace it, first disassociate the current profile:"
        ASSOC_ID=$(aws ec2 describe-iam-instance-profile-associations \
            --filters "Name=instance-id,Values=${INSTANCE_ID}" \
            --query 'IamInstanceProfileAssociations[0].AssociationId' \
            --output text \
            --region ${REGION})
        echo "  aws ec2 disassociate-iam-instance-profile --association-id ${ASSOC_ID} --region ${REGION}"
        exit 1
    fi
fi

echo ""
echo "=========================================="
echo "Setup complete!"
echo "=========================================="
echo ""
echo "The ${MACHINE_NAME} instance now has IAM permissions for:"
echo ""
echo "YACE (CloudWatch Exporter):"
echo "  - cloudwatch:GetMetricData"
echo "  - cloudwatch:GetMetricStatistics"
echo "  - cloudwatch:ListMetrics"
echo "  - tag:GetResources"
echo "  - Various resource discovery permissions"
echo ""
echo "OpenTelemetry Collector (S3 logs):"
echo "  - s3:PutObject, s3:GetObject, s3:ListBucket"
echo "  - Bucket: s3://${S3_LOGS_BUCKET}/"
echo ""
echo "Instance: ${INSTANCE_ID}"
echo "Role: ${INSTANCE_ROLE_NAME}"
echo "Profile: ${INSTANCE_PROFILE_NAME}"
echo "=========================================="
