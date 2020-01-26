/*
 * Copyright 2019 ICON Foundation
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package foundation.icon.test.score;

import foundation.icon.icx.IconService;
import foundation.icon.icx.Wallet;
import foundation.icon.icx.data.Bytes;
import foundation.icon.icx.data.TransactionResult;
import foundation.icon.icx.transport.jsonrpc.RpcItem;
import foundation.icon.icx.transport.jsonrpc.RpcObject;
import foundation.icon.icx.transport.jsonrpc.RpcValue;
import foundation.icon.test.common.Constants;
import foundation.icon.test.common.Env;
import foundation.icon.test.common.ResultTimeoutException;
import foundation.icon.test.common.TransactionFailureException;

import java.io.IOException;
import java.math.BigInteger;
import java.util.HashMap;
import java.util.LinkedList;
import java.util.List;
import java.util.Map;

public class GovScore extends Score {
    private final Wallet governorWallet;
    private final Score chainScore;

    public static class Fee {
        Map<String, BigInteger> stepCosts;
        Map<String, BigInteger> stepMaxLimits;
        BigInteger stepPrice;
    }

    public static String[] stepCostTypes = {
            "default",
            "contractCall",
            "contractCreate",
            "contractUpdate",
            "contractDestruct",
            "contractSet",
            "get",
            "set",
            "replace",
            "delete",
            "input",
            "eventLog",
            "apiCall"
    };
    final long stepLimit = 1000000;

    public GovScore(IconService iconService, Env.Chain chain) {
        super(iconService, chain, Constants.GOV_ADDRESS);
        this.governorWallet = chain.governorWallet;
        this.chainScore = new Score(iconService, chain, Constants.CHAINSCORE_ADDRESS);
    }

    private Wallet getGovernorWallet() {
        return this.governorWallet;
    }

    public void setStepPrice(BigInteger price) throws Exception{
        RpcObject params = new RpcObject.Builder()
                .put("price", new RpcValue(price))
                .build();
        invokeAndWaitResult(getGovernorWallet(), "setStepPrice", params, 0, stepLimit);
    }

    public void setStepCost(String type, BigInteger cost) throws ResultTimeoutException, IOException{
        RpcObject params = new RpcObject.Builder()
                .put("type", new RpcValue(type))
                .put("cost", new RpcValue(cost))
                .build();
        invokeAndWaitResult(getGovernorWallet(), "setStepCost", params, 0, stepLimit);
    }

    public void setMaxStepLimit(String type, BigInteger cost) throws ResultTimeoutException, IOException{
        RpcObject params = new RpcObject.Builder()
                .put("contextType", new RpcValue(type))
                .put("limit", new RpcValue(cost))
                .build();
        invokeAndWaitResult(getGovernorWallet(), "setMaxStepLimit", params, 0, stepLimit);
    }

    public TransactionResult acceptScore(Bytes txHash) throws ResultTimeoutException, IOException {
        RpcObject params = new RpcObject.Builder()
                .put("txHash", new RpcValue(txHash))
                .build();
        return invokeAndWaitResult(getGovernorWallet(), "acceptScore", params, 0, stepLimit);
    }

    public TransactionResult rejectScore(Bytes txHash) throws ResultTimeoutException, IOException {
        RpcObject params = new RpcObject.Builder()
                .put("txHash", new RpcValue(txHash))
                .build();
        return invokeAndWaitResult(getGovernorWallet(), "rejectScore", params, 0, stepLimit);
    }

    public Map<String, BigInteger> getStepCosts() throws Exception {
        RpcItem rpcItem = this.chainScore.call("getStepCosts", null);
        Map<String, BigInteger> map = new HashMap<>();
        for(String type : stepCostTypes) {
            map.put(type, rpcItem.asObject().getItem(type).asInteger());
        }
        return map;
    }

    public void setStepCosts(Map<String, BigInteger> map)
            throws ResultTimeoutException, TransactionFailureException, IOException {
        List<Bytes> list = new LinkedList<>();
        for(String type : map.keySet()) {
            RpcObject params = new RpcObject.Builder()
                    .put("type", new RpcValue(type))
                    .put("cost", new RpcValue(map.get(type)))
                    .build();
            Bytes txHash = invoke(getGovernorWallet(), "setStepCost", params, 0, stepLimit);
            list.add(txHash);
        }
        for(Bytes txHash : list) {
            TransactionResult result = getResult(txHash);
            if (!Constants.STATUS_SUCCESS.equals(result.getStatus())) {
                throw new TransactionFailureException(result.getFailure());
            }
        }
    }

    public Map<String, BigInteger> getMaxStepLimits() throws Exception {
        Map<String, BigInteger> map = new HashMap<>();
        String[] types = {"invoke", "query"};
        for(String t : types) {
            RpcObject params = new RpcObject.Builder()
                    .put("contextType", new RpcValue(t))
                    .build();
            BigInteger stepLimit = this.chainScore.call("getMaxStepLimit", params).asInteger();
            map.put(t, stepLimit);
        }
        return map;
    }

    public void setMaxStepLimits(Map<String, BigInteger> limits)
            throws ResultTimeoutException, TransactionFailureException, IOException {
        List<Bytes> list = new LinkedList<>();
        for(String type : limits.keySet()) {
            RpcObject params = new RpcObject.Builder()
                    .put("contextType", new RpcValue(type))
                    .put("limit", new RpcValue(limits.get(type)))
                    .build();
            Bytes txHash = invoke(getGovernorWallet(), "setMaxStepLimit", params, 0, stepLimit);
            list.add(txHash);
        }
        for(Bytes txHash : list) {
            TransactionResult result = getResult(txHash);
            if (!Constants.STATUS_SUCCESS.equals(result.getStatus())) {
                throw new TransactionFailureException(result.getFailure());
            }
        }
    }

    public Fee getFee() throws Exception {
        Fee fee = new Fee();
        fee.stepCosts = getStepCosts();
        fee.stepMaxLimits = getMaxStepLimits();
        fee.stepPrice = this.chainScore.call("getStepPrice", null).asInteger();
        return fee;
    }

    public void setFee(Fee fee) throws Exception {
        setStepPrice(fee.stepPrice);
        setStepCosts(fee.stepCosts);
        setMaxStepLimits(fee.stepMaxLimits);
    }
}
