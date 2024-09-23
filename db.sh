gcloud auth login
gcloud auth application-default login

PROJECT_ID="gcp-cin-gemini-270001"
REGION="us-central1"
DATABASE_NAME=""
PORT="3306"

environment=$1
environment=$(echo "$environment" | tr '[:upper:]' '[:lower:]') #translate to lowercase

gcloud config set project $PROJECT_ID
gcloud auth application-default set-quota-project $PROJECT_ID

case "$environment" in
"dev")
  echo "development"
  DATABASE_NAME="cintas-gemini-db-dev2"
;;
"qa")
  echo "qas"
  DATABASE_NAME="cintas-gemini-db-qa";;
"prod")
  echo "production"
  DATABASE_NAME="cintas-gemini-db";;
*)
  echo "Unknown environment"
  exit 1;
esac

echo cloud-sql-proxy --port $PORT --run-connection-test $PROJECT_ID:$REGION:$DATABASE_NAME
cloud-sql-proxy --port $PORT --run-connection-test $PROJECT_ID:$REGION:$DATABASE_NAME
