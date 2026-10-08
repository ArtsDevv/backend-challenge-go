#!/usr/bin/env bash
set -euo pipefail

ENDPOINT_URL="${AWS_ENDPOINT_URL:-http://localhost:4566}"
REGION="${AWS_REGION:-us-east-1}"
MAX_RECEIVE_COUNT="${SQS_MAX_RECEIVE_COUNT:-5}"
VISIBILITY_TIMEOUT="${SQS_VISIBILITY_TIMEOUT:-30}"

awscmd() {
  if command -v awslocal >/dev/null 2>&1; then
    awslocal "$@"
  else
    aws --endpoint-url "$ENDPOINT_URL" --region "$REGION" "$@"
  fi
}

queue_url() {
  local queue_name="$1"
  awscmd sqs get-queue-url --queue-name "$queue_name" --query 'QueueUrl' --output text
}

queue_arn() {
  local url="$1"
  awscmd sqs get-queue-attributes \
    --queue-url "$url" \
    --attribute-names QueueArn \
    --query 'Attributes.QueueArn' \
    --output text
}

create_dlq() {
  local queue_name="$1"
  echo "Creating DLQ: ${queue_name}" >&2
  awscmd sqs create-queue \
    --queue-name "$queue_name" \
    --attributes "{\"FifoQueue\":\"true\",\"ContentBasedDeduplication\":\"true\"}" \
    >/dev/null
  local url
  url=$(queue_url "$queue_name")
  queue_arn "$url"
}

create_main_queue() {
  local queue_name="$1"
  local dlq_arn="$2"

  local redrive_policy
  redrive_policy=$(printf '{\"deadLetterTargetArn\":\"%s\",\"maxReceiveCount\":\"%s\"}' \
    "$dlq_arn" "$MAX_RECEIVE_COUNT")

  echo "Creating queue: ${queue_name} (DLQ=${dlq_arn}, maxReceiveCount=${MAX_RECEIVE_COUNT})" >&2
  awscmd sqs create-queue \
    --queue-name "$queue_name" \
    --attributes "{
      \"FifoQueue\": \"true\",
      \"ContentBasedDeduplication\": \"true\",
      \"VisibilityTimeout\": \"${VISIBILITY_TIMEOUT}\",
      \"RedrivePolicy\": \"$(printf '%s' "$redrive_policy" | sed 's/"/\\"/g')\"
    }" \
    >/dev/null
}

main() {
  echo "Provisioning SQS FIFO queues against ${ENDPOINT_URL} (region=${REGION})"

  local wt_dlq_arn
  wt_dlq_arn=$(create_dlq "wager-transactions-dlq.fifo")
  create_main_queue "wager-transactions.fifo" "$wt_dlq_arn"

  local we_dlq_arn
  we_dlq_arn=$(create_dlq "wager-events-dlq.fifo")
  create_main_queue "wager-events.fifo" "$we_dlq_arn"

  echo "SQS FIFO queues ready: wager-transactions.fifo, wager-transactions-dlq.fifo, wager-events.fifo, wager-events-dlq.fifo"
}

main "$@"
