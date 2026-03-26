#!/bin/sh

cd "$(dirname "$0")/.."

COMPOSE="docker compose -f compose.test.yml"
GATEWAY_PORT=2222
SSH_CFG=.test/ssh_config

pass=0
fail=0

ok() {
  pass=$((pass + 1))
  printf "  PASS: %s\n" "$1"
}

ng() {
  fail=$((fail + 1))
  printf "  FAIL: %s\n" "$1"
}

run_test() {
  printf "\n== %s ==\n" "$1"
}

ssh_jump() {
  user=$1 key=$2 cmd=$3
  cat > "$SSH_CFG" <<EOF
Host gateway
  HostName localhost
  Port $GATEWAY_PORT

Host backend
  ProxyJump gateway

Host *
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
  IdentityFile $key
  IdentitiesOnly yes
EOF
  ssh -F "$SSH_CFG" -J "${user}@gateway" "${user}@backend" "$cmd"
}

reload_gateway() {
  cat > .test/gateway-config/config.yaml
  printf "  reloading gateway...\n"
  $COMPOSE kill -s HUP gateway
  printf "  reloaded\n"
  sleep 1
}

cleanup() {
  $COMPOSE down --remove-orphans
  rm -rf .test
}
trap cleanup EXIT

rm -rf .test
mkdir -p .test/gateway-config .test/keys \
  .test/alice/.ssh .test/bob/.ssh

ssh-keygen -t ed25519 -f .test/keys/id_alice -N "" -C "alice@laptop" > /dev/null 2>&1
ssh-keygen -t ed25519 -f .test/keys/id_alice_new -N "" -C "alice@new-laptop" > /dev/null 2>&1
ssh-keygen -t ed25519 -f .test/keys/id_bob -N "" -C "bob@desktop" > /dev/null 2>&1

ALICE_KEY=.test/keys/id_alice
ALICE_PUB=$(cat .test/keys/id_alice.pub)
ALICE_NEW_KEY=.test/keys/id_alice_new
ALICE_NEW_PUB=$(cat .test/keys/id_alice_new.pub)
BOB_KEY=.test/keys/id_bob
BOB_PUB=$(cat .test/keys/id_bob.pub)

cp .test/keys/id_alice.pub .test/alice/.ssh/authorized_keys
cp .test/keys/id_bob.pub .test/bob/.ssh/authorized_keys
chmod 644 .test/alice/.ssh/authorized_keys .test/bob/.ssh/authorized_keys

printf "Building and starting services...\n"
$COMPOSE up -d --build

printf "Waiting for gateway...\n"
i=0
while [ "$i" -lt 30 ]; do
  $COMPOSE logs gateway 2>/dev/null | grep -q "sshd started" && break
  sleep 1
  i=$((i + 1))
done

# --- Test 1: Add alice ---
run_test "Add alice and SSH jump"

reload_gateway <<EOF
project: 'test'
users:
  - name: 'alice'
    keys:
      - '$ALICE_PUB'
EOF

if ssh_jump alice "$ALICE_KEY" "echo jump-ok" 2>/dev/null | grep -q "jump-ok"; then
  ok "alice can jump through gateway"
else
  ng "alice jump through gateway failed"
fi

# --- Test 2: Add bob ---
run_test "Add bob alongside alice"

reload_gateway <<EOF
project: 'test'
users:
  - name: 'alice'
    keys:
      - '$ALICE_PUB'
  - name: 'bob'
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump bob "$BOB_KEY" "echo bob-ok" 2>/dev/null | grep -q "bob-ok"; then
  ok "bob can jump through gateway"
else
  ng "bob jump through gateway failed"
fi

if ssh_jump alice "$ALICE_KEY" "echo alice-still-ok" 2>/dev/null | grep -q "alice-still-ok"; then
  ok "alice still works after adding bob"
else
  ng "alice broken after adding bob"
fi

# --- Test 3: Rotate alice's key ---
run_test "Rotate alice's key"

cp .test/keys/id_alice_new.pub .test/alice/.ssh/authorized_keys

reload_gateway <<EOF
project: 'test'
users:
  - name: 'alice'
    keys:
      - '$ALICE_NEW_PUB'
  - name: 'bob'
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump alice "$ALICE_KEY" "echo should-fail" 2>/dev/null | grep -q "should-fail"; then
  ng "alice's old key should be rejected"
else
  ok "alice's old key correctly rejected"
fi

if ssh_jump alice "$ALICE_NEW_KEY" "echo alice-new-ok" 2>/dev/null | grep -q "alice-new-ok"; then
  ok "alice's new key works"
else
  ng "alice's new key failed"
fi

if ssh_jump bob "$BOB_KEY" "echo bob-still-ok" 2>/dev/null | grep -q "bob-still-ok"; then
  ok "bob unaffected by alice's key rotation"
else
  ng "bob broken by alice's key rotation"
fi

# --- Test 4: Remove alice ---
run_test "Remove alice"

reload_gateway <<EOF
project: 'test'
users:
  - name: 'bob'
    keys:
      - '$BOB_PUB'
EOF

if ssh_jump alice "$ALICE_KEY" "echo should-fail" 2>/dev/null | grep -q "should-fail"; then
  ng "removed alice should be rejected"
else
  ok "removed alice correctly rejected"
fi

if ssh_jump bob "$BOB_KEY" "echo bob-remaining-ok" 2>/dev/null | grep -q "bob-remaining-ok"; then
  ok "bob still works after removing alice"
else
  ng "bob broken after removing alice"
fi

# --- Test 5: Direct shell access denied ---
run_test "Shell access denied (ForceCommand)"

cat > "$SSH_CFG" <<EOF
Host gateway
  HostName localhost
  Port $GATEWAY_PORT

Host *
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
  IdentityFile $BOB_KEY
  IdentitiesOnly yes
EOF
if ssh -F "$SSH_CFG" "bob@gateway" echo "shell-ok" 2>/dev/null | grep -q "shell-ok"; then
  ng "direct shell access should be denied"
else
  ok "direct shell access correctly denied"
fi

# --- Summary ---
printf "\n== Results: %d passed, %d failed ==\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
